package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func getEnvAny(keys []string, def string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return def
}

type Config struct {
	Port                string
	JioApiUrl           string
	YtApiKey            string
	YtCookies           string
	YtCookiesB64        string
	TelegramToken       string
	TelegramChatID      string
	TelegramFileID      string
	TelegramMusicChatID string
	SubUser             string
	SubPass             string
	SubUsersRaw         string
	DbPath              string
}

func loadConfig() Config {
	c := Config{
		Port:                os.Getenv("PORT"),
		JioApiUrl:           getEnvAny([]string{"JIOSAAVN_API_URL", "JIO_API_URL"}, "https://jiosaavn-api-three-ashy.vercel.app"),
		YtApiKey:            getEnvAny([]string{"YT_API_KEY", "YOUTUBE_API_KEY"}, ""),
		YtCookies:           getEnvAny([]string{"YT_COOKIES", "YOUTUBE_COOKIES"}, ""),
		YtCookiesB64:        getEnvAny([]string{"YT_COOKIES_B64", "YOUTUBE_COOKIES_B64"}, ""),
		TelegramToken:       getEnvAny([]string{"TELEGRAM_BOT_TOKEN", "BOT_TOKEN", "TG_BOT_TOKEN", "TELEGRAM_TOKEN"}, ""),
		TelegramChatID:      getEnvAny([]string{"TELEGRAM_CHAT_ID", "CHANNEL_ID", "TG_CHAT_ID", "CHAT_ID"}, ""),
		TelegramFileID:      getEnvAny([]string{"TELEGRAM_DB_FILE_ID", "DB_JSON_FILE_ID", "DB_JSON_FILE", "DB_FILE_ID", "TELEGRAM_FILE_ID", "TG_DB_FILE_ID", "TELEGRAM_DB_FILE"}, ""),
		TelegramMusicChatID: getEnvAny([]string{"TELEGRAM_MUSIC_CHAT_ID", "MUSIC_CHAT_ID", "TG_MUSIC_CHAT_ID"}, ""),
		SubUser:             getEnvAny([]string{"SUBSONIC_USER", "ADMIN_USER"}, "admin"),
		SubPass:             getEnvAny([]string{"SUBSONIC_PASSWORD", "ADMIN_PASS"}, "admin"),
		SubUsersRaw:         getEnvAny([]string{"SUBSONIC_USERS", "USERS"}, ""),
		DbPath:              getEnvAny([]string{"DB_PATH", "DATABASE_PATH"}, "db.json"),
	}
	if c.Port == "" {
		c.Port = "8000"
	}
	c.JioApiUrl = strings.TrimSuffix(c.JioApiUrl, "/")
	if c.TelegramMusicChatID == "" {
		c.TelegramMusicChatID = c.TelegramChatID
	}
	return c
}

var cfg Config
var usersMap = map[string]string{}
var muUsers sync.RWMutex

func getCookiesContent() string {
	if b64 := getEnvAny([]string{"YT_COOKIES_B64", "YOUTUBE_COOKIES_B64"}, ""); strings.TrimSpace(b64) != "" {
		trimmed := strings.TrimSpace(b64)
		if d, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
			s := string(d)
			if strings.Contains(s, "Netscape") || strings.Contains(s, "youtube.com") || len(s) > 100 {
				return s
			}
		}
		if d, err := base64.RawStdEncoding.DecodeString(trimmed); err == nil {
			s := string(d)
			if len(s) > 100 {
				return s
			}
		}
		if strings.Contains(trimmed, "Netscape") {
			return trimmed
		}
		return trimmed
	}
	raw := getEnvAny([]string{"YT_COOKIES", "YOUTUBE_COOKIES"}, "")
	if raw == "" {
		return ""
	}
	trimmed := strings.TrimSpace(raw)
	if strings.Contains(trimmed, "Netscape") {
		return trimmed
	}
	if d, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
		s := string(d)
		if strings.Contains(s, "Netscape") {
			return s
		}
	}
	return trimmed
}

func initUsers() {
	muUsers.Lock()
	defer muUsers.Unlock()
	usersMap = map[string]string{}
	usersMap[cfg.SubUser] = cfg.SubPass
	if cfg.SubUsersRaw != "" {
		for _, p := range strings.Split(cfg.SubUsersRaw, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			kv := strings.SplitN(p, ":", 2)
			if len(kv) == 2 {
				usersMap[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
			}
		}
	}
	if db.Users != nil {
		for u, pw := range db.Users {
			if _, ok := usersMap[u]; !ok {
				usersMap[u] = pw
			}
		}
	}
}

type Song struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Artist         string `json:"artist"`
	ArtistID       string `json:"artistId"`
	Album          string `json:"album"`
	AlbumID        string `json:"albumId"`
	Duration       int    `json:"duration"`
	CoverArt       string `json:"coverArt"`
	Year           int    `json:"year"`
	Genre          string `json:"genre"`
	Source         string `json:"source"`
	SourceID       string `json:"sourceId"`
	StreamURL      string `json:"streamUrl,omitempty"`
	ThumbURL       string `json:"thumbUrl"`
	PlayCount      int    `json:"playCount"`
	TgFileID       string `json:"tgFileId,omitempty"`
	TgFileUniqueID string `json:"tgFileUniqueId,omitempty"`
	CachedAt       string `json:"cachedAt,omitempty"`
}
type Artist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Album struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Artist   string   `json:"artist"`
	ArtistID string   `json:"artistId"`
	CoverArt string   `json:"coverArt"`
	SongIDs  []string `json:"songIds"`
	ThumbURL string   `json:"thumbUrl"`
	Year     int      `json:"year"`
}
type Playlist struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Owner     string   `json:"owner"`
	Comment   string   `json:"comment"`
	SongIDs   []string `json:"songIds"`
	Public    bool     `json:"public"`
	Created   string   `json:"created"`
	Source    string   `json:"source"`
	SourceURL string   `json:"sourceUrl"`
}
type DB struct {
	sync.RWMutex
	Songs     map[string]*Song           `json:"songs"`
	Artists   map[string]*Artist         `json:"artists"`
	Albums    map[string]*Album          `json:"albums"`
	Playlists map[string]*Playlist       `json:"playlists"`
	Users     map[string]string          `json:"users"`
	Starred   map[string]map[string]bool `json:"starred"`
}

var db = &DB{
	Songs:     make(map[string]*Song),
	Artists:   make(map[string]*Artist),
	Albums:    make(map[string]*Album),
	Playlists: make(map[string]*Playlist),
	Users:     make(map[string]string),
	Starred:   make(map[string]map[string]bool),
}
var streamCache sync.Map
type cachedURL struct{ URL string; Expiry time.Time }
var ytSem = make(chan struct{}, 2)
var tgCacheInProgress sync.Map
var httpClient = &http.Client{Timeout: 10 * time.Second}

type ConnStatus struct {
	TG struct {
		Connected bool; Working bool; Error string; BotName string; ChatID string
	} `json:"tg"`
	YT struct {
		Connected bool; Working bool; Error string; HasKey bool; HasCookies bool; YtdlpVersion string
	} `json:"yt"`
	Jio struct {
		Connected bool; Working bool; Error string; ApiUrl string
	} `json:"jio"`
	Cookies struct{ Exists bool; Size int; Valid bool } `json:"cookies"`
}

func getConnectionsFast() ConnStatus {
	var status ConnStatus
	status.TG.Connected = cfg.TelegramToken != "" && cfg.TelegramChatID != ""
	status.TG.ChatID = cfg.TelegramChatID
	if cfg.TelegramToken != "" {
		status.TG.Working = true
		status.TG.BotName = "Sbmuz_bot"
		if cfg.TelegramChatID == "" {
			status.TG.Working = false
			status.TG.Error = "CHANNEL_ID missing"
		}
	} else {
		status.TG.Error = "BOT_TOKEN missing"
	}
	status.YT.HasKey = cfg.YtApiKey != ""
	status.YT.Connected = cfg.YtApiKey != ""
	status.YT.Working = cfg.YtApiKey != ""
	if cfg.YtApiKey == "" {
		status.YT.Error = "YT_API_KEY missing"
	}
	cc := getCookiesContent()
	if cc != "" {
		status.YT.HasCookies = true
		status.Cookies.Exists = true
		status.Cookies.Size = len(cc)
		status.Cookies.Valid = true
	}
	status.Jio.ApiUrl = cfg.JioApiUrl
	status.Jio.Connected = cfg.JioApiUrl != ""
	status.Jio.Working = cfg.JioApiUrl != ""
	cmd := exec.Command("yt-dlp", "--version")
	out, _ := cmd.CombinedOutput()
	status.YT.YtdlpVersion = strings.TrimSpace(string(out))
	return status
}

func (d *DB) save() error {
	d.RLock()
	defer d.RUnlock()
	dir := filepath.Dir(cfg.DbPath)
	os.MkdirAll(dir, 0755)
	b, _ := json.MarshalIndent(d, "", "  ")
	tmp := cfg.DbPath + ".tmp"
	os.WriteFile(tmp, b, 0644)
	return os.Rename(tmp, cfg.DbPath)
}
func (d *DB) load() error {
	if _, err := os.Stat(cfg.DbPath); err != nil {
		if _, err2 := os.Stat("db.json"); err2 == nil {
			cfg.DbPath = "db.json"
		} else {
			return err
		}
	}
	b, _ := os.ReadFile(cfg.DbPath)
	var tmp DB
	json.Unmarshal(b, &tmp)
	d.Lock()
	if tmp.Songs != nil {
		d.Songs = tmp.Songs
	}
	if tmp.Artists != nil {
		d.Artists = tmp.Artists
	}
	if tmp.Albums != nil {
		d.Albums = tmp.Albums
	}
	if tmp.Playlists != nil {
		d.Playlists = tmp.Playlists
	}
	if tmp.Users != nil {
		d.Users = tmp.Users
	}
	if tmp.Starred != nil {
		d.Starred = tmp.Starred
	}
	if d.Starred == nil {
		d.Starred = make(map[string]map[string]bool)
	}
	d.Unlock()
	return nil
}

func telegramUploadDB() {
	if cfg.TelegramToken == "" || cfg.TelegramChatID == "" {
		return
	}
	if _, err := os.Stat(cfg.DbPath); err != nil {
		return
	}
	f, _ := os.Open(cfg.DbPath)
	defer f.Close()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	w.WriteField("chat_id", cfg.TelegramChatID)
	w.WriteField("caption", fmt.Sprintf("backup %s songs:%d", time.Now().Format(time.RFC3339), len(db.Songs)))
	part, _ := w.CreateFormFile("document", "db.json")
	io.Copy(part, f)
	w.Close()
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", cfg.TelegramToken)
	req, _ := http.NewRequest("POST", url, body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	client := &http.Client{Timeout: 30 * time.Second}
	resp, _ := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}
}
func countCached() int {
	c := 0
	db.RLock()
	for _, s := range db.Songs {
		if s.TgFileID != "" {
			c++
		}
	}
	db.RUnlock()
	return c
}
func telegramDownloadDB() error {
	if cfg.TelegramToken == "" || cfg.TelegramFileID == "" {
		return fmt.Errorf("no file_id")
	}
	u := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", cfg.TelegramToken, cfg.TelegramFileID)
	resp, err := httpClient.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		Ok     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	if r.Result.FilePath == "" {
		return fmt.Errorf("empty path - file_id expired? get new file_id from TG channel")
	}
	down := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", cfg.TelegramToken, r.Result.FilePath)
	resp2, _ := httpClient.Get(down)
	if resp2 == nil {
		return fmt.Errorf("download nil")
	}
	defer resp2.Body.Close()
	b, _ := io.ReadAll(resp2.Body)
	if len(b) < 10 {
		return fmt.Errorf("db file too small %d bytes", len(b))
	}
	os.MkdirAll(filepath.Dir(cfg.DbPath), 0755)
	return os.WriteFile(cfg.DbPath, b, 0644)
}
func telegramGetFileUrl(fileID string) (string, error) {
	if cfg.TelegramToken == "" {
		return "", fmt.Errorf("no token")
	}
	u := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", cfg.TelegramToken, fileID)
	resp, err := httpClient.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Ok     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.Result.FilePath == "" {
		return "", fmt.Errorf("empty file_path")
	}
	return fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", cfg.TelegramToken, r.Result.FilePath), nil
}
func telegramUploadAudioFile(filePathOrUrl string, song *Song) (string, string, error) {
	if cfg.TelegramToken == "" || cfg.TelegramMusicChatID == "" {
		return "", "", fmt.Errorf("tg not configured")
	}
	filename := fmt.Sprintf("%s - %s.m4a", song.Artist, song.Title)
	filename = strings.ReplaceAll(filename, "/", "_")
	var data []byte
	if strings.HasPrefix(filePathOrUrl, "http") {
		resp, err := httpClient.Get(filePathOrUrl)
		if err != nil {
			return "", "", err
		}
		defer resp.Body.Close()
		data, _ = io.ReadAll(io.LimitReader(resp.Body, 48*1024*1024))
	} else {
		f, err := os.Open(filePathOrUrl)
		if err != nil {
			return "", "", err
		}
		defer f.Close()
		data, _ = io.ReadAll(io.LimitReader(f, 48*1024*1024))
	}
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	w.WriteField("chat_id", cfg.TelegramMusicChatID)
	w.WriteField("caption", fmt.Sprintf("🎵 %s - %s | %s", song.Artist, song.Title, song.ID))
	part, _ := w.CreateFormFile("audio", filename)
	part.Write(data)
	w.Close()
	apiUrl := fmt.Sprintf("https://api.telegram.org/bot%s/sendAudio", cfg.TelegramToken)
	req, _ := http.NewRequest("POST", apiUrl, body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var res struct {
		Ok     bool `json:"ok"`
		Result struct {
			Audio struct {
				FileID       string `json:"file_id"`
				FileUniqueID string `json:"file_unique_id"`
			} `json:"audio"`
			Document struct {
				FileID       string `json:"file_id"`
				FileUniqueID string `json:"file_unique_id"`
			} `json:"document"`
		} `json:"result"`
		Description string `json:"description"`
	}
	json.Unmarshal(b, &res)
	if !res.Ok {
		body2 := &bytes.Buffer{}
		w2 := multipart.NewWriter(body2)
		w2.WriteField("chat_id", cfg.TelegramMusicChatID)
		w2.WriteField("caption", fmt.Sprintf("🎵 %s - %s | %s", song.Artist, song.Title, song.ID))
		part2, _ := w2.CreateFormFile("document", filename)
		part2.Write(data)
		w2.Close()
		apiUrl2 := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", cfg.TelegramToken)
		req2, _ := http.NewRequest("POST", apiUrl2, body2)
		req2.Header.Set("Content-Type", w2.FormDataContentType())
		resp2, err2 := client.Do(req2)
		if err2 != nil {
			return "", "", fmt.Errorf("audio fail: %s", res.Description)
		}
		defer resp2.Body.Close()
		b2, _ := io.ReadAll(resp2.Body)
		var res2 struct {
			Ok     bool `json:"ok"`
			Result struct {
				Document struct {
					FileID       string `json:"file_id"`
					FileUniqueID string `json:"file_unique_id"`
				} `json:"document"`
			} `json:"result"`
		}
		json.Unmarshal(b2, &res2)
		if !res2.Ok {
			return "", "", fmt.Errorf("tg upload failed")
		}
		return res2.Result.Document.FileID, res2.Result.Document.FileUniqueID, nil
	}
	fid := res.Result.Audio.FileID
	if fid == "" {
		fid = res.Result.Document.FileID
	}
	fuid := res.Result.Audio.FileUniqueID
	if fuid == "" {
		fuid = res.Result.Document.FileUniqueID
	}
	if fid == "" {
		return "", "", fmt.Errorf("no file_id")
	}
	return fid, fuid, nil
}
func cacheSongToTelegram(s *Song, streamUrl string) {
	if s.TgFileID != "" {
		return
	}
	if _, inProg := tgCacheInProgress.Load(s.ID); inProg {
		return
	}
	tgCacheInProgress.Store(s.ID, true)
	defer tgCacheInProgress.Delete(s.ID)
	fid, fuid, err := telegramUploadAudioFile(streamUrl, s)
	if err != nil {
		log.Printf("TG cache failed %s: %v", s.ID, err)
		return
	}
	db.Lock()
	if song, ok := db.Songs[s.ID]; ok {
		song.TgFileID = fid
		song.TgFileUniqueID = fuid
		song.CachedAt = time.Now().Format(time.RFC3339)
	}
	db.Unlock()
	db.save()
	go telegramUploadDB()
}
func checkAuth(r *http.Request) (bool, string) {
	q := r.URL.Query()
	u := q.Get("u")
	if u == "" {
		u = q.Get("username")
	}
	p := q.Get("p")
	t := q.Get("t")
	s := q.Get("s")
	if u == "" {
		if strings.Contains(r.URL.Path, "ping") {
			return true, cfg.SubUser
		}
		muUsers.RLock()
		if len(usersMap) == 1 {
			for user := range usersMap {
				muUsers.RUnlock()
				return true, user
			}
		}
		muUsers.RUnlock()
		return false, ""
	}
	muUsers.RLock()
	exp, ok := usersMap[u]
	muUsers.RUnlock()
	if !ok {
		return false, ""
	}
	if t != "" && s != "" {
		h := md5.New()
		h.Write([]byte(exp + s))
		if fmt.Sprintf("%x", h.Sum(nil)) == t {
			return true, u
		}
		return false, ""
	}
	if p != "" {
		if strings.HasPrefix(p, "enc:") {
			if d, err := hex.DecodeString(p[4:]); err == nil {
				p = string(d)
			}
		}
		if p == exp {
			return true, u
		}
	}
	return false, ""
}
func writeJSON(w http.ResponseWriter, s int, p interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(s)
	json.NewEncoder(w).Encode(p)
}
func subOK(d map[string]interface{}) map[string]interface{} {
	b := map[string]interface{}{
		"status": "ok", "version": "1.16.1", "type": "substreamer-compatible",
		"serverVersion": "5.2-substreamer-search-fix", "openSubsonic": true,
	}
	for k, v := range d {
		b[k] = v
	}
	return map[string]interface{}{"subsonic-response": b}
}
func subFail(m string, c int) map[string]interface{} {
	return map[string]interface{}{
		"subsonic-response": map[string]interface{}{
			"status": "failed", "version": "1.16.1",
			"error": map[string]interface{}{"code": c, "message": m},
		},
	}
}
func respond(w http.ResponseWriter, r *http.Request, d map[string]interface{}) {
	log.Printf("REQ %s %s ?u=%s q=%s UA=%s", r.Method, r.URL.Path, r.URL.Query().Get("u"), r.URL.Query().Get("query"), r.Header.Get("User-Agent"))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	writeJSON(w, 200, subOK(d))
}
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func slugArtist(n string) string {
	return "ar_" + url.PathEscape(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(n), " ", "_")))
}
func slugAlbum(n, a string) string {
	return "al_" + url.PathEscape(strings.ToLower(strings.ReplaceAll(a+"_"+n, " ", "_")))
}
func ensureArtist(n string) *Artist {
	if n == "" {
		n = "Unknown Artist"
	}
	id := slugArtist(n)
	db.RLock()
	if a, ok := db.Artists[id]; ok {
		db.RUnlock()
		return a
	}
	db.RUnlock()
	a := &Artist{ID: id, Name: n}
	db.Lock()
	db.Artists[id] = a
	db.Unlock()
	return a
}
func ensureAlbum(name, artist, artistId, thumb string) *Album {
	if name == "" {
		name = "Unknown Album"
	}
	id := slugAlbum(name, artist)
	db.RLock()
	if al, ok := db.Albums[id]; ok {
		db.RUnlock()
		return al
	}
	db.RUnlock()
	al := &Album{ID: id, Name: name, Artist: artist, ArtistID: artistId, CoverArt: id, ThumbURL: thumb, SongIDs: []string{}}
	db.Lock()
	db.Albums[id] = al
	db.Unlock()
	return al
}
func contains(arr []string, s string) bool {
	for _, v := range arr {
		if v == s {
			return true
		}
	}
	return false
}
func parseJioImage(img interface{}) string {
	switch v := img.(type) {
	case string:
		return v
	case []interface{}:
		if len(v) > 0 {
			if m, ok := v[len(v)-1].(map[string]interface{}); ok {
				if u, ok := m["url"].(string); ok {
					return u
				}
			}
		}
	}
	return ""
}
func parseJioDownloadUrl(d interface{}) string {
	if s, ok := d.(string); ok {
		return s
	}
	if arr, ok := d.([]interface{}); ok {
		var best string
		for _, it := range arr {
			if m, ok := it.(map[string]interface{}); ok {
				link, _ := m["link"].(string)
				if link == "" {
					link, _ = m["url"].(string)
				}
				if q, _ := m["quality"].(string); strings.Contains(q, "320") {
					return link
				}
				best = link
			}
		}
		return best
	}
	return ""
}

// FIXED searchJio with safe parsing - no panic
func searchJio(query string) ([]*Song, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	endpoints := []string{
		fmt.Sprintf("%s/search?query=%s", cfg.JioApiUrl, url.QueryEscape(query)),
		fmt.Sprintf("%s/search/songs?query=%s&limit=40", cfg.JioApiUrl, url.QueryEscape(query)),
		fmt.Sprintf("https://saavn.dev/api/search/songs?query=%s", url.QueryEscape(query)),
	}
	var allSongs []*Song
	for _, ep := range endpoints {
		resp, err := httpClient.Get(ep)
		if err != nil {
			log.Printf("JIO endpoint fail %s: %v", ep, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if len(body) < 10 {
			continue
		}
		var g map[string]interface{}
		if err := json.Unmarshal(body, &g); err != nil {
			continue
		}
		var raw []interface{}
		if data, ok := g["data"].(map[string]interface{}); ok {
			if res, ok := data["results"].([]interface{}); ok {
				raw = res
			} else if res, ok := data["songs"].(map[string]interface{}); ok {
				if results, ok := res["results"].([]interface{}); ok {
					raw = results
				}
			}
		} else if data, ok := g["data"].([]interface{}); ok {
			raw = data
		} else if results, ok := g["results"].([]interface{}); ok {
			raw = results
		}
		if len(raw) == 0 {
			continue
		}
		for _, rs := range raw {
			m, ok := rs.(map[string]interface{})
			if !ok || m == nil {
				continue
			}
			title, _ := m["name"].(string)
			if title == "" {
				title, _ = m["title"].(string)
			}
			if title == "" {
				continue
			}
			// SAFE artist parsing - no panic
			artist := ""
			if pa, ok := m["primaryArtists"].(string); ok && pa != "" {
				artist = pa
			} else {
				if artistsField, ok := m["artists"]; ok && artistsField != nil {
					if artistsMap, ok := artistsField.(map[string]interface{}); ok {
						if primary, ok := artistsMap["primary"].(string); ok && primary != "" {
							artist = primary
						} else if primaryArr, ok := artistsMap["primary"].([]interface{}); ok && len(primaryArr) > 0 {
							if first, ok := primaryArr[0].(map[string]interface{}); ok {
								if name, ok := first["name"].(string); ok {
									artist = name
								}
							}
						}
					}
				}
			}
			if artist == "" {
				artist = "Various"
			}
			id, _ := m["id"].(string)
			if id == "" {
				continue
			}
			thumb := parseJioImage(m["image"])
			artistObj := ensureArtist(artist)
			albumObj := ensureAlbum(title+" Single", artist, artistObj.ID, thumb)
			s := &Song{
				ID: "jio_" + id, Title: title, Artist: artist, ArtistID: artistObj.ID,
				Album: albumObj.Name, AlbumID: albumObj.ID, CoverArt: albumObj.ID,
				Source: "jio", SourceID: id, ThumbURL: thumb, Duration: 210,
			}
			if dl, ok := m["downloadUrl"]; ok {
				s.StreamURL = parseJioDownloadUrl(dl)
			}
			allSongs = append(allSongs, s)
			db.Lock()
			db.Songs[s.ID] = s
			if !contains(albumObj.SongIDs, s.ID) {
				albumObj.SongIDs = append(albumObj.SongIDs, s.ID)
			}
			db.Unlock()
		}
		if len(allSongs) >= 20 {
			break
		}
	}
	if len(allSongs) > 0 {
		go func() { db.save(); go telegramUploadDB() }()
	}
	log.Printf("JIO search '%s' -> %d songs", query, len(allSongs))
	return allSongs, nil
}

func getJioStream(jioId string) (string, error) {
	if v, ok := streamCache.Load("jio_" + jioId); ok {
		c := v.(cachedURL)
		if time.Now().Before(c.Expiry) {
			return c.URL, nil
		}
	}
	endpoints := []string{
		fmt.Sprintf("%s/songs?id=%s", cfg.JioApiUrl, jioId),
		fmt.Sprintf("https://saavn.dev/api/songs/%s", jioId),
	}
	for _, ep := range endpoints {
		resp, err := httpClient.Get(ep)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var g map[string]interface{}
		json.Unmarshal(body, &g)
		var dlUrl string
		if data, ok := g["data"].([]interface{}); ok {
			for _, it := range data {
				if m, ok := it.(map[string]interface{}); ok {
					if dl, ok := m["downloadUrl"]; ok {
						dlUrl = parseJioDownloadUrl(dl)
						if dlUrl != "" {
							break
						}
					}
				}
			}
		} else if data, ok := g["data"].(map[string]interface{}); ok {
			if dl, ok := data["downloadUrl"]; ok {
				dlUrl = parseJioDownloadUrl(dl)
			}
		}
		if dlUrl != "" {
			streamCache.Store("jio_"+jioId, cachedURL{URL: dlUrl, Expiry: time.Now().Add(time.Hour)})
			return dlUrl, nil
		}
	}
	return "", fmt.Errorf("jio not found")
}

type YTSearch struct {
	Items []struct {
		ID struct {
			VideoID string `json:"videoId"`
		} `json:"id"`
		Snippet struct {
			Title        string `json:"title"`
			ChannelTitle string `json:"channelTitle"`
			Thumbnails   map[string]struct{ URL string `json:"url"` } `json:"thumbnails"`
		} `json:"snippet"`
	} `json:"items"`
}

func searchYoutube(q string) ([]*Song, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	if cfg.YtApiKey == "" {
		return nil, fmt.Errorf("YT_API_KEY missing")
	}
	u := fmt.Sprintf("https://www.googleapis.com/youtube/v3/search?part=snippet&type=video&videoCategoryId=10&maxResults=40&q=%s&key=%s", url.QueryEscape(q), cfg.YtApiKey)
	resp, err := httpClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var yt YTSearch
	if err := json.NewDecoder(resp.Body).Decode(&yt); err != nil {
		return nil, err
	}
	var songs []*Song
	for _, it := range yt.Items {
		if it.ID.VideoID == "" {
			continue
		}
		thumb := ""
		if t, ok := it.Snippet.Thumbnails["high"]; ok {
			thumb = t.URL
		} else if t, ok := it.Snippet.Thumbnails["medium"]; ok {
			thumb = t.URL
		}
		art := ensureArtist(it.Snippet.ChannelTitle)
		al := ensureAlbum(it.Snippet.ChannelTitle+" - YouTube", it.Snippet.ChannelTitle, art.ID, thumb)
		s := &Song{
			ID: "yt_" + it.ID.VideoID, Title: it.Snippet.Title, Artist: it.Snippet.ChannelTitle,
			ArtistID: art.ID, Album: al.Name, AlbumID: al.ID, CoverArt: al.ID,
			Source: "yt", SourceID: it.ID.VideoID, ThumbURL: thumb, Duration: 210,
		}
		songs = append(songs, s)
		db.Lock()
		if existing, ok := db.Songs[s.ID]; ok {
			if existing.TgFileID != "" {
				s.TgFileID = existing.TgFileID
				s.TgFileUniqueID = existing.TgFileUniqueID
				s.CachedAt = existing.CachedAt
			}
		}
		db.Songs[s.ID] = s
		if !contains(al.SongIDs, s.ID) {
			al.SongIDs = append(al.SongIDs, s.ID)
		}
		db.Unlock()
	}
	go func() { db.save(); go telegramUploadDB() }()
	log.Printf("YT search '%s' -> %d songs", q, len(songs))
	return songs, nil
}

func getYoutubeStream(vid string) (string, error) {
	if v, ok := streamCache.Load("yt_" + vid); ok {
		c := v.(cachedURL)
		if time.Now().Before(c.Expiry) {
			return c.URL, nil
		}
	}
	ytSem <- struct{}{}
	defer func() { <-ytSem }()
	cookieContent := getCookiesContent()
	cookiePath := ""
	if cookieContent != "" {
		cookiePath = "/tmp/cookies.txt"
		os.WriteFile(cookiePath, []byte(cookieContent), 0600)
	}
	for attempt := 0; attempt < 2; attempt++ {
		args := []string{"--no-playlist", "--get-url", "-f", "bestaudio[ext=m4a]/bestaudio/best", "https://www.youtube.com/watch?v=" + vid}
		if attempt == 0 && cookiePath != "" {
			args = append([]string{"--cookies", cookiePath}, args...)
		}
		cmd := exec.Command("yt-dlp", args...)
		var out bytes.Buffer
		var errBuf bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errBuf
		done := make(chan error, 1)
		go func() { done <- cmd.Run() }()
		select {
		case err := <-done:
			if err == nil {
				for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
					if strings.HasPrefix(strings.TrimSpace(line), "http") {
						streamCache.Store("yt_"+vid, cachedURL{URL: strings.TrimSpace(line), Expiry: time.Now().Add(30 * time.Minute)})
						return strings.TrimSpace(line), nil
					}
				}
			} else {
				log.Printf("YT attempt %d fail %s: %v", attempt, vid, err)
			}
		case <-time.After(25 * time.Second):
			cmd.Process.Kill()
			log.Printf("YT timeout %s attempt %d", vid, attempt)
		}
	}
	return "", fmt.Errorf("no url after retries")
}

func extractPlaylistID(input string) string {
	if strings.HasPrefix(input, "PL") && !strings.Contains(input, "http") {
		return input
	}
	u, err := url.Parse(input)
	if err == nil {
		if list := u.Query().Get("list"); list != "" {
			return list
		}
	}
	re := regexp.MustCompile(`[?&]list=([a-zA-Z0-9_-]+)`)
	m := re.FindStringSubmatch(input)
	if len(m) > 1 {
		return m[1]
	}
	return input
}
func importYoutubePlaylist(pid string, owner string) (*Playlist, error) {
	if cfg.YtApiKey == "" {
		return nil, fmt.Errorf("YT_API_KEY missing")
	}
	pid = extractPlaylistID(pid)
	title := pid
	infoURL := fmt.Sprintf("https://www.googleapis.com/youtube/v3/playlists?part=snippet&id=%s&key=%s", pid, cfg.YtApiKey)
	if resp, err := httpClient.Get(infoURL); err == nil {
		defer resp.Body.Close()
		var pr struct {
			Items []struct {
				Snippet struct{ Title string `json:"title"` } `json:"snippet"`
			} `json:"items"`
		}
		json.NewDecoder(resp.Body).Decode(&pr)
		if len(pr.Items) > 0 {
			title = pr.Items[0].Snippet.Title
		}
	}
	var allIDs []string
	pageToken := ""
	for {
		apiUrl := fmt.Sprintf("https://www.googleapis.com/youtube/v3/playlistItems?part=snippet&maxResults=50&playlistId=%s&key=%s", pid, cfg.YtApiKey)
		if pageToken != "" {
			apiUrl += "&pageToken=" + pageToken
		}
		resp, err := httpClient.Get(apiUrl)
		if err != nil {
			break
		}
		var pr struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				Snippet struct {
					ResourceID struct{ VideoID string `json:"videoId"` } `json:"resourceId"`
					Title        string `json:"title"`
					ChannelTitle string `json:"channelTitle"`
					Thumbnails   map[string]struct{ URL string `json:"url"` } `json:"thumbnails"`
				} `json:"snippet"`
			} `json:"items"`
		}
		json.NewDecoder(resp.Body).Decode(&pr)
		resp.Body.Close()
		for _, it := range pr.Items {
			if it.Snippet.ResourceID.VideoID == "" {
				continue
			}
			vid := it.Snippet.ResourceID.VideoID
			allIDs = append(allIDs, vid)
			thumb := ""
			if t, ok := it.Snippet.Thumbnails["high"]; ok {
				thumb = t.URL
			}
			art := ensureArtist(it.Snippet.ChannelTitle)
			al := ensureAlbum(it.Snippet.ChannelTitle+" - YouTube", it.Snippet.ChannelTitle, art.ID, thumb)
			sID := "yt_" + vid
			s := &Song{ID: sID, Title: it.Snippet.Title, Artist: it.Snippet.ChannelTitle, ArtistID: art.ID, Album: al.Name, AlbumID: al.ID, CoverArt: al.ID, Source: "yt", SourceID: vid, ThumbURL: thumb, Duration: 210}
			db.Lock()
			if existing, ok := db.Songs[s.ID]; ok && existing.TgFileID != "" {
				s.TgFileID = existing.TgFileID
				s.TgFileUniqueID = existing.TgFileUniqueID
				s.CachedAt = existing.CachedAt
			}
			db.Songs[s.ID] = s
			if !contains(al.SongIDs, s.ID) {
				al.SongIDs = append(al.SongIDs, s.ID)
			}
			db.Unlock()
		}
		if pr.NextPageToken == "" {
			break
		}
		pageToken = pr.NextPageToken
		if len(allIDs) > 500 {
			break
		}
	}
	if len(allIDs) == 0 {
		return nil, fmt.Errorf("no videos")
	}
	plID := "pl_" + pid
	pl := &Playlist{
		ID: plID, Name: title, Owner: owner,
		Comment: fmt.Sprintf("YT Import %d songs", len(allIDs)), SongIDs: []string{},
		Created: time.Now().Format(time.RFC3339), Source: "yt",
		SourceURL: "https://www.youtube.com/playlist?list=" + pid,
	}
	for _, vid := range allIDs {
		pl.SongIDs = append(pl.SongIDs, "yt_"+vid)
	}
	db.Lock()
	db.Playlists[pl.ID] = pl
	db.Unlock()
	db.save()
	go telegramUploadDB()
	return pl, nil
}

func handlePing(w http.ResponseWriter, r *http.Request) { respond(w, r, map[string]interface{}{}) }
func handleLicense(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{"license": map[string]interface{}{"valid": true}})
}
func handleMusicFolders(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{"musicFolders": map[string]interface{}{"musicFolder": []map[string]interface{}{{"id": 0, "name": "Music"}}}})
}
func handleGetIndexes(w http.ResponseWriter, r *http.Request) {
	db.RLock()
	defer db.RUnlock()
	idx := map[string][]map[string]interface{}{}
	for _, a := range db.Artists {
		letter := "#"
		if len(a.Name) > 0 {
			letter = strings.ToUpper(string(a.Name[0]))
		}
		idx[letter] = append(idx[letter], map[string]interface{}{"id": a.ID, "name": a.Name})
	}
	var indexes []map[string]interface{}
	for k, v := range idx {
		indexes = append(indexes, map[string]interface{}{"name": k, "artist": v})
	}
	respond(w, r, map[string]interface{}{"indexes": map[string]interface{}{"index": indexes, "lastModified": time.Now().Unix() * 1000}})
}
func handleGetUser(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	respond(w, r, map[string]interface{}{"user": map[string]interface{}{"username": user, "adminRole": user == cfg.SubUser}})
}
func handleGetArtists(w http.ResponseWriter, r *http.Request) {
	db.RLock()
	defer db.RUnlock()
	idx := map[string][]map[string]interface{}{}
	for _, a := range db.Artists {
		letter := "#"
		if len(a.Name) > 0 {
			letter = strings.ToUpper(string(a.Name[0]))
		}
		idx[letter] = append(idx[letter], map[string]interface{}{"id": a.ID, "name": a.Name})
	}
	var indexes []map[string]interface{}
	for k, v := range idx {
		indexes = append(indexes, map[string]interface{}{"name": k, "artist": v})
	}
	respond(w, r, map[string]interface{}{"artists": map[string]interface{}{"index": indexes}})
}
func handleGetArtist(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	db.RLock()
	a, ok := db.Artists[id]
	if !ok {
		db.RUnlock()
		writeJSON(w, 200, subFail("not found", 70))
		return
	}
	var albums []map[string]interface{}
	for _, al := range db.Albums {
		if al.ArtistID == id {
			albums = append(albums, map[string]interface{}{"id": al.ID, "name": al.Name, "songCount": len(al.SongIDs), "coverArt": al.ID})
		}
	}
	db.RUnlock()
	respond(w, r, map[string]interface{}{"artist": map[string]interface{}{"id": a.ID, "name": a.Name, "album": albums}})
}
func handleGetAlbum(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	db.RLock()
	al, ok := db.Albums[id]
	if !ok {
		if s, ok2 := db.Songs[id]; ok2 {
			al, ok = db.Albums[s.AlbumID]
		}
	}
	if !ok {
		db.RUnlock()
		writeJSON(w, 200, subFail("not found", 70))
		return
	}
	var songs []map[string]interface{}
	for _, sid := range al.SongIDs {
		if s, ok := db.Songs[sid]; ok {
			songs = append(songs, songToSubsonic(s, r))
		}
	}
	db.RUnlock()
	respond(w, r, map[string]interface{}{"album": map[string]interface{}{"id": al.ID, "name": al.Name, "artist": al.Artist, "coverArt": al.ID, "song": songs, "songCount": len(songs)}})
}
func handleGetSong(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	db.RLock()
	s, ok := db.Songs[id]
	db.RUnlock()
	if !ok {
		writeJSON(w, 200, subFail("not found", 70))
		return
	}
	respond(w, r, map[string]interface{}{"song": songToSubsonic(s, r)})
}
func songToSubsonic(s *Song, r *http.Request) map[string]interface{} {
	sourceTag := ""
	if s.Source == "yt" {
		sourceTag = "[YT] "
	} else if s.Source == "jio" {
		sourceTag = "[JIO] "
	}
	titleWithTag := sourceTag + s.Title
	duration := s.Duration
	if duration == 0 {
		duration = 210
	}
	m := map[string]interface{}{
		"id": s.ID, "title": titleWithTag, "artist": s.Artist, "album": s.Album,
		"albumId": s.AlbumID, "artistId": s.ArtistID, "coverArt": s.CoverArt,
		"duration": duration, "contentType": "audio/mp4", "suffix": "m4a",
		"genre": s.Source, "comment": s.Source,
	}
	if s.TgFileID != "" {
		m["cached"] = "tg"
	}
	return m
}
func handleSearch3(w http.ResponseWriter, r *http.Request) {
	if ok, _ := checkAuth(r); !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	q := r.URL.Query().Get("query")
	if strings.TrimSpace(q) == "" {
		log.Printf("SEARCH empty query -> returning empty")
		respond(w, r, map[string]interface{}{"searchResult3": map[string]interface{}{"song": []interface{}{}}})
		return
	}
	sCount, _ := strconv.Atoi(r.URL.Query().Get("songCount"))
	if sCount == 0 {
		sCount = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("songOffset"))
	if offset < 0 {
		offset = 0
	}
	if sCount < 20 {
		sCount = 50
	}
	if sCount > 100 {
		sCount = 100
	}
	var jio, yt []*Song
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); jio, _ = searchJio(q) }()
	go func() { defer wg.Done(); yt, _ = searchYoutube(q) }()
	wg.Wait()
	all := []*Song{}
	maxLen := len(jio)
	if len(yt) > maxLen {
		maxLen = len(yt)
	}
	for i := 0; i < maxLen; i++ {
		if i < len(jio) {
			all = append(all, jio[i])
		}
		if i < len(yt) {
			all = append(all, yt[i])
		}
	}
	if offset > 0 && offset < len(all) {
		all = all[offset:]
	}
	if len(all) > sCount {
		all = all[:sCount]
	}
	var res []map[string]interface{}
	for _, s := range all {
		res = append(res, songToSubsonic(s, r))
	}
	log.Printf("SEARCH '%s' -> JIO:%d YT:%d Total:%d Offset:%d", q, len(jio), len(yt), len(res), offset)
	respond(w, r, map[string]interface{}{"searchResult3": map[string]interface{}{"song": res}})
}
func handleAlbumList2(w http.ResponseWriter, r *http.Request) {
	db.RLock()
	var albums []*Album
	for _, al := range db.Albums {
		albums = append(albums, al)
	}
	db.RUnlock()
	rand.Shuffle(len(albums), func(i, j int) { albums[i], albums[j] = albums[j], albums[i] })
	if len(albums) > 20 {
		albums = albums[:20]
	}
	var res []map[string]interface{}
	for _, al := range albums {
		res = append(res, map[string]interface{}{"id": al.ID, "name": al.Name, "artist": al.Artist, "coverArt": al.ID, "songCount": len(al.SongIDs)})
	}
	respond(w, r, map[string]interface{}{"albumList2": map[string]interface{}{"album": res}})
}
func handleRandomSongs(w http.ResponseWriter, r *http.Request) {
	db.RLock()
	var all []*Song
	for _, s := range db.Songs {
		all = append(all, s)
	}
	db.RUnlock()
	rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[i], all[j] })
	if len(all) > 20 {
		all = all[:20]
	}
	var res []map[string]interface{}
	for _, s := range all {
		res = append(res, songToSubsonic(s, r))
	}
	respond(w, r, map[string]interface{}{"randomSongs": map[string]interface{}{"song": res}})
}
func handleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	log.Printf("STREAM REQ: %s ?u=%s id=%s", r.URL.Path, r.URL.Query().Get("u"), r.URL.Query().Get("id"))
	if ok, _ := checkAuth(r); !ok {
		log.Printf("STREAM AUTH FAIL for %s", r.URL.Query().Get("u"))
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", 400)
		return
	}
	db.RLock()
	s, ok := db.Songs[id]
	if !ok {
		// Try raw ID handling - substreamer sometimes sends raw video ID without prefix
		for _, tryID := range []string{"yt_" + id, "jio_" + id, id} {
			if song, ok2 := db.Songs[tryID]; ok2 {
				s = song
				ok = true
				id = tryID
				break
			}
		}
	}
	db.RUnlock()
	if !ok {
		// Auto-recreate for any ID that looks like YT video ID (11 chars) or longer
		log.Printf("STREAM 404: id=%s not found, db has %d songs - auto-creating", id, len(db.Songs))
		vid := id
		source := "yt"
		if strings.HasPrefix(id, "yt_") {
			vid = strings.TrimPrefix(id, "yt_")
			source = "yt"
		} else if strings.HasPrefix(id, "jio_") {
			vid = strings.TrimPrefix(id, "jio_")
			source = "jio"
		} else if len(id) == 11 || (len(id) > 8 && !strings.Contains(id, "_")) {
			vid = id
			source = "yt"
			id = "yt_" + vid
		}
		s = &Song{
			ID: id, Title: "YT - " + vid, Artist: "YouTube", Album: "YouTube",
			Source: source, SourceID: vid, Duration: 210,
		}
		db.Lock()
		db.Songs[id] = s
		db.Songs[vid] = s
		db.Unlock()
		ok = true
	}
	db.Lock()
	if _, ok := db.Songs[id]; ok {
		db.Songs[id].PlayCount++
	}
	db.Unlock()
	if s.TgFileID != "" {
		if tgUrl, err := telegramGetFileUrl(s.TgFileID); err == nil {
			req, _ := http.NewRequest("GET", tgUrl, nil)
			resp, err := httpClient.Do(req)
			if err == nil {
				defer resp.Body.Close()
				w.Header().Set("Content-Type", "audio/mp4")
				w.Header().Set("X-Cache", "TG")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Content-Disposition", "inline")
				log.Printf("STREAM TG HIT for %s", id)
				io.Copy(w, resp.Body)
				return
			}
		}
	}
	var urlStr string
	var err error
	if s.Source == "jio" {
		urlStr, err = getJioStream(s.SourceID)
		if err != nil || urlStr == "" {
			urlStr = s.StreamURL
		}
		if urlStr == "" {
			log.Printf("STREAM JIO no url for %s", id)
			http.Error(w, "jio stream fail", 502)
			return
		}
	} else {
		urlStr, err = getYoutubeStream(s.SourceID)
		if err != nil || urlStr == "" {
			log.Printf("STREAM YT fail for %s (%s): %v", id, s.SourceID, err)
			http.Error(w, "yt stream fail: "+err.Error(), 502)
			return
		}
	}
	if s.TgFileID == "" && s.Source == "yt" {
		go cacheSongToTelegram(s, urlStr)
	}
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("STREAM upstream fail for %s: %v", id, err)
		http.Error(w, "upstream fail", 502)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		log.Printf("STREAM upstream status %d for %s: %s", resp.StatusCode, id, string(body))
		http.Error(w, fmt.Sprintf("upstream %d", resp.StatusCode), 502)
		return
	}
	w.Header().Set("Content-Type", "audio/mp4")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Cache", "MISS")
	w.Header().Set("Cache-Control", "no-cache")
	log.Printf("STREAM MISS OK for %s", id)
	io.Copy(w, resp.Body)
}
func handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if ok, _ := checkAuth(r); !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	id := r.URL.Query().Get("id")
	db.RLock()
	s, ok := db.Songs[id]
	db.RUnlock()
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	var urlStr string
	if s.TgFileID != "" {
		if tgUrl, err := telegramGetFileUrl(s.TgFileID); err == nil {
			urlStr = tgUrl
		}
	}
	if urlStr == "" {
		if s.Source == "jio" {
			urlStr, _ = getJioStream(s.SourceID)
			if urlStr == "" {
				urlStr = s.StreamURL
			}
		} else {
			var err error
			urlStr, err = getYoutubeStream(s.SourceID)
			if err != nil || urlStr == "" {
				http.Error(w, "stream fail", 502)
				return
			}
		}
	}
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, "upstream", 502)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "audio/mp4")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s - %s.m4a\"", s.Artist, s.Title))
	io.Copy(w, resp.Body)
}
func handleCoverArt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	var thumb string
	db.RLock()
	if al, ok := db.Albums[id]; ok {
		thumb = al.ThumbURL
	} else if s, ok := db.Songs[id]; ok {
		thumb = s.ThumbURL
	}
	db.RUnlock()
	if thumb == "" {
		http.Redirect(w, r, "https://via.placeholder.com/500x500.png?text=No+Art", 302)
		return
	}
	resp, _ := httpClient.Get(thumb)
	if resp == nil {
		http.Redirect(w, r, thumb, 302)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	io.Copy(w, resp.Body)
}
func handleGetPlaylists(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	db.RLock()
	defer db.RUnlock()
	var list []map[string]interface{}
	for _, pl := range db.Playlists {
		if pl.Owner == user || pl.Public || user == cfg.SubUser {
			list = append(list, map[string]interface{}{"id": pl.ID, "name": pl.Name, "owner": pl.Owner, "songCount": len(pl.SongIDs), "created": pl.Created})
		}
	}
	respond(w, r, map[string]interface{}{"playlists": map[string]interface{}{"playlist": list}})
}
func handleGetPlaylist(w http.ResponseWriter, r *http.Request) {
	if ok, _ := checkAuth(r); !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	id := r.URL.Query().Get("id")
	db.RLock()
	pl, ok := db.Playlists[id]
	db.RUnlock()
	if !ok {
		writeJSON(w, 200, subFail("not found", 70))
		return
	}
	db.RLock()
	var songs []map[string]interface{}
	for _, sid := range pl.SongIDs {
		if s, ok := db.Songs[sid]; ok {
			songs = append(songs, songToSubsonic(s, r))
		}
	}
	db.RUnlock()
	respond(w, r, map[string]interface{}{"playlist": map[string]interface{}{"id": pl.ID, "name": pl.Name, "owner": pl.Owner, "songCount": len(songs), "entry": songs, "created": pl.Created}})
}
func handleCreatePlaylist(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "New Playlist"
	}
	id := "pl_" + fmt.Sprintf("%d", time.Now().UnixNano())
	pl := &Playlist{ID: id, Name: name, Owner: user, SongIDs: []string{}, Created: time.Now().Format(time.RFC3339)}
	db.Lock()
	db.Playlists[id] = pl
	db.Unlock()
	db.save()
	respond(w, r, map[string]interface{}{"playlist": map[string]interface{}{"id": pl.ID, "name": pl.Name}})
}
func handleUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	id := r.URL.Query().Get("playlistId")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	name := r.URL.Query().Get("name")
	toAdd := r.URL.Query()["songIdToAdd"]
	toRemoveIdxStr := r.URL.Query()["songIndexToRemove"]
	db.Lock()
	pl, ok := db.Playlists[id]
	if !ok {
		db.Unlock()
		writeJSON(w, 200, subFail("not found", 70))
		return
	}
	if pl.Owner != user && user != cfg.SubUser {
		db.Unlock()
		writeJSON(w, 200, subFail("not owner", 50))
		return
	}
	if name != "" {
		pl.Name = name
	}
	for _, sid := range toAdd {
		if _, exists := db.Songs[sid]; exists {
			if !contains(pl.SongIDs, sid) {
				pl.SongIDs = append(pl.SongIDs, sid)
			}
		}
	}
	if len(toRemoveIdxStr) > 0 {
		var idxs []int
		for _, s := range toRemoveIdxStr {
			if i, err := strconv.Atoi(s); err == nil {
				idxs = append(idxs, i)
			}
		}
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				if idxs[a] < idxs[b] {
					idxs[a], idxs[b] = idxs[b], idxs[a]
				}
			}
		}
		for _, idx := range idxs {
			if idx >= 0 && idx < len(pl.SongIDs) {
				pl.SongIDs = append(pl.SongIDs[:idx], pl.SongIDs[idx+1:]...)
			}
		}
	}
	db.Unlock()
	db.save()
	go telegramUploadDB()
	respond(w, r, map[string]interface{}{})
}
func handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	id := r.URL.Query().Get("id")
	db.Lock()
	if pl, ok := db.Playlists[id]; ok {
		if pl.Owner == user || user == cfg.SubUser {
			delete(db.Playlists, id)
		}
	}
	db.Unlock()
	db.save()
	respond(w, r, map[string]interface{}{})
}
func handleImportYT(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	u := r.URL.Query().Get("url")
	if u == "" {
		u = r.URL.Query().Get("id")
	}
	if u == "" {
		u = r.URL.Query().Get("playlistId")
	}
	pl, err := importYoutubePlaylist(u, user)
	if err != nil {
		writeJSON(w, 200, subFail(err.Error(), 0))
		return
	}
	respond(w, r, map[string]interface{}{"playlist": map[string]interface{}{"id": pl.ID, "name": pl.Name, "songCount": len(pl.SongIDs)}})
}
func handleCreateUser(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	if user != cfg.SubUser {
		writeJSON(w, 200, subFail("only admin", 50))
		return
	}
	newU := r.URL.Query().Get("username")
	newP := r.URL.Query().Get("password")
	if newU == "" || newP == "" {
		writeJSON(w, 200, subFail("username password required", 10))
		return
	}
	muUsers.Lock()
	usersMap[newU] = newP
	muUsers.Unlock()
	db.Lock()
	db.Users[newU] = newP
	db.Unlock()
	db.save()
	go telegramUploadDB()
	respond(w, r, map[string]interface{}{"user": map[string]interface{}{"username": newU}})
}
func handleGetUsers(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok || user != cfg.SubUser {
		writeJSON(w, 200, subFail("admin only", 50))
		return
	}
	muUsers.RLock()
	var list []map[string]interface{}
	for u := range usersMap {
		list = append(list, map[string]interface{}{"username": u, "adminRole": u == cfg.SubUser})
	}
	muUsers.RUnlock()
	respond(w, r, map[string]interface{}{"users": map[string]interface{}{"user": list}})
}
func handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok || user != cfg.SubUser {
		writeJSON(w, 200, subFail("admin only", 50))
		return
	}
	delU := r.URL.Query().Get("username")
	if delU == cfg.SubUser {
		writeJSON(w, 200, subFail("cannot delete admin", 10))
		return
	}
	muUsers.Lock()
	delete(usersMap, delU)
	muUsers.Unlock()
	db.Lock()
	delete(db.Users, delU)
	db.Unlock()
	db.save()
	respond(w, r, map[string]interface{}{})
}
func handleStar(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	ids := r.URL.Query()["id"]
	db.Lock()
	if _, ok := db.Starred[user]; !ok {
		db.Starred[user] = make(map[string]bool)
	}
	for _, id := range ids {
		db.Starred[user][id] = true
	}
	db.Unlock()
	db.save()
	respond(w, r, map[string]interface{}{})
}
func handleUnstar(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	ids := r.URL.Query()["id"]
	db.Lock()
	if m, ok := db.Starred[user]; ok {
		for _, id := range ids {
			delete(m, id)
		}
	}
	db.Unlock()
	db.save()
	respond(w, r, map[string]interface{}{})
}
func handleGetStarred(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	db.RLock()
	starMap := db.Starred[user]
	var songs []map[string]interface{}
	for sid := range starMap {
		if s, ok := db.Songs[sid]; ok {
			songs = append(songs, songToSubsonic(s, r))
		}
	}
	db.RUnlock()
	respond(w, r, map[string]interface{}{"starred": map[string]interface{}{"song": songs}})
}
func handleScrobble(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id != "" {
		db.Lock()
		if s, ok := db.Songs[id]; ok {
			s.PlayCount++
		}
		db.Unlock()
	}
	respond(w, r, map[string]interface{}{})
}
func handleScanStatus(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{"scanStatus": map[string]interface{}{"scanning": false, "count": len(db.Songs)}})
}
func handleRestCatchAll(w http.ResponseWriter, r *http.Request) {
	log.Printf("CATCH-ALL REQ: %s %s UA=%s", r.Method, r.URL.Path, r.Header.Get("User-Agent"))
	respond(w, r, map[string]interface{}{})
}
func handleConnections(w http.ResponseWriter, r *http.Request) {
	if ok, _ := checkAuth(r); !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	status := getConnectionsFast()
	db.RLock()
	songsCount := len(db.Songs)
	cachedCount := 0
	for _, s := range db.Songs {
		if s.TgFileID != "" {
			cachedCount++
		}
	}
	db.RUnlock()
	respond(w, r, map[string]interface{}{
		"connections": status,
		"stats": map[string]interface{}{"songs": songsCount, "cached": cachedCount, "playlists": len(db.Playlists), "users": len(usersMap)},
	})
}
func handleTgIndex(w http.ResponseWriter, r *http.Request) {
	if ok, _ := checkAuth(r); !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	db.RLock()
	var list []map[string]interface{}
	for _, s := range db.Songs {
		list = append(list, map[string]interface{}{
			"id": s.ID, "title": s.Title, "artist": s.Artist, "source": s.Source, "sourceId": s.SourceID,
			"tgFileId": s.TgFileID, "cached": s.TgFileID != "", "cachedAt": s.CachedAt,
			"thumb": s.ThumbURL, "playCount": s.PlayCount,
		})
	}
	db.RUnlock()
	respond(w, r, map[string]interface{}{"tgIndex": list, "total": len(list), "cached": countCached()})
}

func main() {
	cfg = loadConfig()
	log.Println("=== SUBSTREAMER v5.2 FINAL - SEARCH FIX + DB 0 FIX + RAW ID ===")
	log.Printf("YT_API_KEY len=%d cookies size=%d", len(cfg.YtApiKey), len(getCookiesContent()))

	// Try to load DB from file first, then from TG
	db.load()
	log.Printf("DB from file: %d songs", len(db.Songs))
	if len(db.Songs) == 0 && cfg.TelegramFileID != "" {
		log.Println("DB empty, trying download from TG...")
		if err := telegramDownloadDB(); err != nil {
			log.Printf("DB download failed: %v (file_id expired? create new backup via /health endpoint)", err)
		} else {
			db.load()
			log.Printf("DB restored from TG: %d songs", len(db.Songs))
		}
	}
	if len(db.Songs) == 0 {
		log.Println("WARNING: DB still 0 songs - search will still work via YT/JIO APIs, but library empty. Import playlist via /rest/importYoutubePlaylist.view")
	}
	os.MkdirAll(filepath.Dir(cfg.DbPath), 0755)
	initUsers()
	if cc := getCookiesContent(); cc != "" {
		os.WriteFile("/tmp/cookies.txt", []byte(cc), 0600)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/rest/ping.view", handlePing)
	mux.HandleFunc("/rest/ping", handlePing)
	mux.HandleFunc("/rest/getLicense.view", handleLicense)
	mux.HandleFunc("/rest/getLicense", handleLicense)
	mux.HandleFunc("/rest/getMusicFolders.view", handleMusicFolders)
	mux.HandleFunc("/rest/getMusicFolders", handleMusicFolders)
	mux.HandleFunc("/rest/getIndexes.view", handleGetIndexes)
	mux.HandleFunc("/rest/getIndexes", handleGetIndexes)
	mux.HandleFunc("/rest/getUser.view", handleGetUser)
	mux.HandleFunc("/rest/getUser", handleGetUser)
	mux.HandleFunc("/rest/getArtists.view", handleGetArtists)
	mux.HandleFunc("/rest/getArtists", handleGetArtists)
	mux.HandleFunc("/rest/getArtist.view", handleGetArtist)
	mux.HandleFunc("/rest/getArtist", handleGetArtist)
	mux.HandleFunc("/rest/getAlbum.view", handleGetAlbum)
	mux.HandleFunc("/rest/getAlbum", handleGetAlbum)
	mux.HandleFunc("/rest/getSong.view", handleGetSong)
	mux.HandleFunc("/rest/getSong", handleGetSong)
	mux.HandleFunc("/rest/search3.view", handleSearch3)
	mux.HandleFunc("/rest/search3", handleSearch3)
	mux.HandleFunc("/rest/search2.view", handleSearch3)
	mux.HandleFunc("/rest/search2", handleSearch3)
	mux.HandleFunc("/rest/getAlbumList.view", handleAlbumList2)
	mux.HandleFunc("/rest/getAlbumList", handleAlbumList2)
	mux.HandleFunc("/rest/getAlbumList2.view", handleAlbumList2)
	mux.HandleFunc("/rest/getAlbumList2", handleAlbumList2)
	mux.HandleFunc("/rest/getRandomSongs.view", handleRandomSongs)
	mux.HandleFunc("/rest/getRandomSongs", handleRandomSongs)
	mux.HandleFunc("/rest/stream.view", handleStream)
	mux.HandleFunc("/rest/stream", handleStream)
	mux.HandleFunc("/rest/download.view", handleDownload)
	mux.HandleFunc("/rest/download", handleDownload)
	mux.HandleFunc("/rest/getCoverArt.view", handleCoverArt)
	mux.HandleFunc("/rest/getCoverArt", handleCoverArt)
	mux.HandleFunc("/rest/getPlaylists.view", handleGetPlaylists)
	mux.HandleFunc("/rest/getPlaylists", handleGetPlaylists)
	mux.HandleFunc("/rest/getPlaylist.view", handleGetPlaylist)
	mux.HandleFunc("/rest/getPlaylist", handleGetPlaylist)
	mux.HandleFunc("/rest/createPlaylist.view", handleCreatePlaylist)
	mux.HandleFunc("/rest/createPlaylist", handleCreatePlaylist)
	mux.HandleFunc("/rest/deletePlaylist.view", handleDeletePlaylist)
	mux.HandleFunc("/rest/deletePlaylist", handleDeletePlaylist)
	mux.HandleFunc("/rest/updatePlaylist.view", handleUpdatePlaylist)
	mux.HandleFunc("/rest/updatePlaylist", handleUpdatePlaylist)
	mux.HandleFunc("/rest/importYoutubePlaylist.view", handleImportYT)
	mux.HandleFunc("/rest/importYoutubePlaylist", handleImportYT)
	mux.HandleFunc("/rest/createUser.view", handleCreateUser)
	mux.HandleFunc("/rest/createUser", handleCreateUser)
	mux.HandleFunc("/rest/getUsers.view", handleGetUsers)
	mux.HandleFunc("/rest/getUsers", handleGetUsers)
	mux.HandleFunc("/rest/deleteUser.view", handleDeleteUser)
	mux.HandleFunc("/rest/deleteUser", handleDeleteUser)
	mux.HandleFunc("/rest/star.view", handleStar)
	mux.HandleFunc("/rest/star", handleStar)
	mux.HandleFunc("/rest/unstar.view", handleUnstar)
	mux.HandleFunc("/rest/unstar", handleUnstar)
	mux.HandleFunc("/rest/getStarred.view", handleGetStarred)
	mux.HandleFunc("/rest/getStarred", handleGetStarred)
	mux.HandleFunc("/rest/getStarred2.view", handleGetStarred)
	mux.HandleFunc("/rest/getStarred2", handleGetStarred)
	mux.HandleFunc("/rest/scrobble.view", handleScrobble)
	mux.HandleFunc("/rest/scrobble", handleScrobble)
	mux.HandleFunc("/rest/getScanStatus.view", handleScanStatus)
	mux.HandleFunc("/rest/getScanStatus", handleScanStatus)
	mux.HandleFunc("/rest/getConnections.view", handleConnections)
	mux.HandleFunc("/rest/getConnections", handleConnections)
	mux.HandleFunc("/rest/tgIndex.view", handleTgIndex)
	mux.HandleFunc("/rest/tgIndex", handleTgIndex)
	mux.HandleFunc("/rest/getOpenSubsonicExtensions.view", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{
			"openSubsonicExtensions": []interface{}{
				map[string]interface{}{"name": "transcode", "versions": []int{1}},
				map[string]interface{}{"name": "formPost", "versions": []int{1}},
			},
		})
	})
	mux.HandleFunc("/rest/getOpenSubsonicExtensions", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{
			"openSubsonicExtensions": []interface{}{
				map[string]interface{}{"name": "transcode", "versions": []int{1}},
				map[string]interface{}{"name": "formPost", "versions": []int{1}},
			},
		})
	})
	mux.HandleFunc("/rest/getGenres.view", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"genres": map[string]interface{}{"genre": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getGenres", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"genres": map[string]interface{}{"genre": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getInternetRadioStations.view", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"internetRadioStations": map[string]interface{}{"internetRadioStation": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getInternetRadioStations", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"internetRadioStations": map[string]interface{}{"internetRadioStation": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getBookmarks.view", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"bookmarks": map[string]interface{}{"bookmark": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getBookmarks", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"bookmarks": map[string]interface{}{"bookmark": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getPodcasts.view", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"podcasts": map[string]interface{}{"podcast": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getPodcasts", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"podcasts": map[string]interface{}{"podcast": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getShares.view", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"shares": map[string]interface{}{"share": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getShares", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"shares": map[string]interface{}{"share": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getChatMessages.view", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"chatMessages": map[string]interface{}{"chatMessage": []interface{}{}}})
	})
	mux.HandleFunc("/rest/getChatMessages", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"chatMessages": map[string]interface{}{"chatMessage": []interface{}{}}})
	})
	mux.HandleFunc("/rest/", handleRestCatchAll)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		cached := 0
		db.RLock()
		for _, s := range db.Songs {
			if s.TgFileID != "" {
				cached++
			}
		}
		songsCount := len(db.Songs)
		plsCount := len(db.Playlists)
		usersCount := len(usersMap)
		db.RUnlock()
		status := getConnectionsFast()
		writeJSON(w, 200, map[string]interface{}{
			"status": "ok", "songs": songsCount, "playlists": plsCount, "users": usersCount, "cached": cached, "connections": status,
			"note": "If songs=0, DB file_id expired. Re-import playlist via /rest/importYoutubePlaylist.view?url=PLAYLIST_URL",
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.URL.Path != "/" {
			log.Printf("UNKNOWN PATH 404: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "<html><body style='background:#000;color:#fff;font-family:monospace;padding:20px'><h2>Koyeb API v5.2 Substreamer</h2><p>SEARCH FIX + DB 0 FIX + RAW ID support<br><br>Search test: /rest/search3.view?u=admin&p=admin2330&v=1.16.1&c=substreamer&f=json&query=kesariya<br><br>If DB 0, import: /rest/importYoutubePlaylist.view?u=admin&p=admin2330&v=1.16.1&c=substreamer&f=json&url=YT_PLAYLIST_URL</p></body></html>")
	})
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			db.save()
			go telegramUploadDB()
		}
	}()
	port := cfg.Port
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	log.Printf("Starting SUBSTREAMER v5.2 FINAL on %s", port)
	log.Fatal(http.ListenAndServe(port, corsMiddleware(mux)))
}
