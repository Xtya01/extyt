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
	UserRating     int    `json:"userRating,omitempty"`
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
	Ratings   map[string]map[string]int  `json:"ratings"` // user -> id -> rating
}

var db = &DB{
	Songs:     make(map[string]*Song),
	Artists:   make(map[string]*Artist),
	Albums:    make(map[string]*Album),
	Playlists: make(map[string]*Playlist),
	Users:     make(map[string]string),
	Starred:   make(map[string]map[string]bool),
	Ratings:   make(map[string]map[string]int),
}
var streamCache sync.Map

type cachedURL struct {
	URL    string
	Expiry time.Time
}

var ytSem = make(chan struct{}, 2)
var tgCacheInProgress sync.Map
var httpClient = &http.Client{Timeout: 15 * time.Second}

type ConnStatus struct {
	TG struct {
		Connected bool
		Working   bool
		Error     string
		BotName   string
		ChatID    string
	} `json:"tg"`
	YT struct {
		Connected    bool
		Working      bool
		Error        string
		HasKey       bool
		HasCookies   bool
		YtdlpVersion string
	} `json:"yt"`
	Jio struct {
		Connected bool
		Working   bool
		Error     string
		ApiUrl    string
	} `json:"jio"`
	Cookies struct {
		Exists bool
		Size   int
		Valid  bool
	} `json:"cookies"`
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
	_ = os.MkdirAll(dir, 0755)
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := cfg.DbPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, cfg.DbPath)
}

func (d *DB) load() error {
	path := cfg.DbPath
	if _, err := os.Stat(path); err != nil {
		if _, err2 := os.Stat("db.json"); err2 == nil {
			path = "db.json"
			cfg.DbPath = "db.json"
		} else {
			return err
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var tmp DB
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	d.Lock()
	defer d.Unlock()
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
	if tmp.Ratings != nil {
		d.Ratings = tmp.Ratings
	}
	if d.Starred == nil {
		d.Starred = make(map[string]map[string]bool)
	}
	if d.Ratings == nil {
		d.Ratings = make(map[string]map[string]int)
	}
	return nil
}

func telegramUploadDB() {
	if cfg.TelegramToken == "" || cfg.TelegramChatID == "" {
		return
	}
	if _, err := os.Stat(cfg.DbPath); err != nil {
		return
	}
	f, err := os.Open(cfg.DbPath)
	if err != nil {
		return
	}
	defer f.Close()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	_ = w.WriteField("chat_id", cfg.TelegramChatID)
	db.RLock()
	songCount := len(db.Songs)
	db.RUnlock()
	_ = w.WriteField("caption", fmt.Sprintf("backup %s songs:%d", time.Now().Format(time.RFC3339), songCount))
	part, err := w.CreateFormFile("document", "db.json")
	if err != nil {
		return
	}
	_, _ = io.Copy(part, f)
	_ = w.Close()
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", cfg.TelegramToken)
	req, err := http.NewRequest("POST", apiURL, body)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		log.Printf("TG DB upload failed: %v", err)
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
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if r.Result.FilePath == "" {
		return fmt.Errorf("empty path - file_id expired? get new file_id from TG channel")
	}
	down := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", cfg.TelegramToken, r.Result.FilePath)
	resp2, err := httpClient.Get(down)
	if err != nil || resp2 == nil {
		return fmt.Errorf("download failed: %v", err)
	}
	defer resp2.Body.Close()
	b, err := io.ReadAll(resp2.Body)
	if err != nil {
		return err
	}
	if len(b) < 10 {
		return fmt.Errorf("db file too small %d bytes", len(b))
	}
	_ = os.MkdirAll(filepath.Dir(cfg.DbPath), 0755)
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
	if len(data) < 1000 {
		return "", "", fmt.Errorf("audio data too small")
	}
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	_ = w.WriteField("chat_id", cfg.TelegramMusicChatID)
	_ = w.WriteField("caption", fmt.Sprintf("🎵 %s - %s | %s", song.Artist, song.Title, song.ID))
	part, _ := w.CreateFormFile("audio", filename)
	_, _ = part.Write(data)
	_ = w.Close()
	apiUrl := fmt.Sprintf("https://api.telegram.org/bot%s/sendAudio", cfg.TelegramToken)
	req, _ := http.NewRequest("POST", apiUrl, body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	client := &http.Client{Timeout: 90 * time.Second}
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
	_ = json.Unmarshal(b, &res)
	if !res.Ok {
		body2 := &bytes.Buffer{}
		w2 := multipart.NewWriter(body2)
		_ = w2.WriteField("chat_id", cfg.TelegramMusicChatID)
		_ = w2.WriteField("caption", fmt.Sprintf("🎵 %s - %s | %s", song.Artist, song.Title, song.ID))
		part2, _ := w2.CreateFormFile("document", filename)
		_, _ = part2.Write(data)
		_ = w2.Close()
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
		_ = json.Unmarshal(b2, &res2)
		if !res2.Ok {
			return "", "", fmt.Errorf("tg upload failed: %s", res.Description)
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
	if s == nil || s.TgFileID != "" {
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
	_ = db.save()
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

	// OpenSubsonic: getOpenSubsonicExtensions should be public
	if strings.Contains(r.URL.Path, "getOpenSubsonicExtensions") {
		return true, cfg.SubUser
	}
	if strings.Contains(r.URL.Path, "ping") {
		return true, cfg.SubUser
	}

	if u == "" {
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
		h := md5.Sum([]byte(exp + s))
		if hex.EncodeToString(h[:]) == strings.ToLower(t) {
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(s)
	_ = json.NewEncoder(w).Encode(p)
}

func subOK(d map[string]interface{}) map[string]interface{} {
	b := map[string]interface{}{
		"status":        "ok",
		"version":       "1.16.1",
		"type":          "substreamer-compatible",
		"serverVersion": "5.3-repaired",
		"openSubsonic":  true,
	}
	for k, v := range d {
		b[k] = v
	}
	return map[string]interface{}{"subsonic-response": b}
}

func subFail(m string, c int) map[string]interface{} {
	return map[string]interface{}{
		"subsonic-response": map[string]interface{}{
			"status":  "failed",
			"version": "1.16.1",
			"error":   map[string]interface{}{"code": c, "message": m},
		},
	}
}

func respond(w http.ResponseWriter, r *http.Request, d map[string]interface{}) {
	log.Printf("REQ %s %s ?u=%s q=%s", r.Method, r.URL.Path, r.URL.Query().Get("u"), r.URL.Query().Get("query"))
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	writeJSON(w, 200, subOK(d))
}

func requireAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	ok, user := checkAuth(r)
	if !ok {
		log.Printf("AUTH FAIL path=%s u=%s hasP=%v hasT=%v",
			r.URL.Path, r.URL.Query().Get("u"),
			r.URL.Query().Get("p") != "", r.URL.Query().Get("t") != "")
		writeJSON(w, 200, subFail("Wrong username or password", 40))
		return "", false
	}
	return user, true
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
		// Log every incoming request (helps debug Substreamer)
		if strings.HasPrefix(r.URL.Path, "/rest/") {
			log.Printf("IN %s %s u=%s query=%s f=%s c=%s",
				r.Method, r.URL.Path,
				r.URL.Query().Get("u"),
				r.URL.Query().Get("query"),
				r.URL.Query().Get("f"),
				r.URL.Query().Get("c"),
			)
		}
		next.ServeHTTP(w, r)
	})
}

func slugArtist(n string) string {
	n = strings.TrimSpace(n)
	if n == "" {
		n = "Unknown"
	}
	return "ar_" + url.PathEscape(strings.ToLower(strings.ReplaceAll(n, " ", "_")))
}

func slugAlbum(n, a string) string {
	n = strings.TrimSpace(n)
	a = strings.TrimSpace(a)
	if n == "" {
		n = "Unknown"
	}
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
			if s, ok := v[len(v)-1].(string); ok {
				return s
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
				if link != "" {
					best = link
				}
			}
		}
		return best
	}
	return ""
}

func searchJio(query string) ([]*Song, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	endpoints := []string{
		fmt.Sprintf("%s/search?query=%s", strings.TrimRight(cfg.JioApiUrl, "/"), url.QueryEscape(query)),
		fmt.Sprintf("https://saavn.sumit.co/api/search/songs?query=%s", url.QueryEscape(query)),
	}
	var allSongs []*Song
	seen := map[string]bool{}
	for _, ep := range endpoints {
		resp, err := httpClient.Get(ep)
		if err != nil {
			log.Printf("JIO endpoint fail %s: %v", ep, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if len(body) < 10 || resp.StatusCode != 200 {
			log.Printf("JIO bad response %s status=%d len=%d", ep, resp.StatusCode, len(body))
			continue
		}
		var g map[string]interface{}
		if err := json.Unmarshal(body, &g); err != nil {
			continue
		}
		var raw []interface{}
		// formats: {data:{results:[]}} | {data:{songs:{results:[]}}} | {data:[]} | {success,data:[]}
		if data, ok := g["data"].(map[string]interface{}); ok {
			if res, ok := data["results"].([]interface{}); ok {
				raw = res
			} else if res, ok := data["songs"].(map[string]interface{}); ok {
				if results, ok := res["results"].([]interface{}); ok {
					raw = results
				}
			} else if res, ok := data["songs"].([]interface{}); ok {
				raw = res
			}
		} else if data, ok := g["data"].([]interface{}); ok {
			raw = data
		} else if results, ok := g["results"].([]interface{}); ok {
			raw = results
		}
		if len(raw) == 0 {
			log.Printf("JIO parse empty results from %s", ep)
			continue
		}
		for _, rs := range raw {
			m, ok := rs.(map[string]interface{})
			if !ok || m == nil {
				continue
			}
			// skip non-song entries from mixed search
			if t, _ := m["type"].(string); t != "" && t != "song" {
				continue
			}
			title, _ := m["name"].(string)
			if title == "" {
				title, _ = m["title"].(string)
			}
			if title == "" {
				continue
			}
			artist := ""
			if pa, ok := m["primaryArtists"].(string); ok && pa != "" {
				artist = pa
			} else if sub, ok := m["subtitle"].(string); ok && sub != "" {
				// "Artist - Album" style
				if i := strings.Index(sub, " - "); i > 0 {
					artist = strings.TrimSpace(sub[:i])
				} else {
					artist = sub
				}
			} else if artistsField, ok := m["artists"]; ok && artistsField != nil {
				if artistsMap, ok := artistsField.(map[string]interface{}); ok {
					if primaryArr, ok := artistsMap["primary"].([]interface{}); ok && len(primaryArr) > 0 {
						if first, ok := primaryArr[0].(map[string]interface{}); ok {
							if name, ok := first["name"].(string); ok {
								artist = name
							}
						}
					}
				}
			}
			if artist == "" {
				if mi, ok := m["more_info"].(map[string]interface{}); ok {
					if music, ok := mi["music"].(string); ok && music != "" {
						artist = music
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
			sid := "jio_" + id
			if seen[sid] {
				continue
			}
			seen[sid] = true
			thumb := parseJioImage(m["image"])
			artistObj := ensureArtist(artist)
			albumName := title
			if alb, ok := m["album"].(map[string]interface{}); ok {
				if an, ok := alb["name"].(string); ok && an != "" {
					albumName = an
				}
			} else if an, ok := m["album"].(string); ok && an != "" {
				albumName = an
			} else if mi, ok := m["more_info"].(map[string]interface{}); ok {
				if an, ok := mi["album"].(string); ok && an != "" {
					albumName = an
				}
			}
			albumObj := ensureAlbum(albumName, artist, artistObj.ID, thumb)
			dur := 210
			if d, ok := m["duration"].(float64); ok && d > 0 {
				dur = int(d)
			} else if d, ok := m["duration"].(string); ok {
				if di, err := strconv.Atoi(d); err == nil && di > 0 {
					dur = di
				}
			} else if mi, ok := m["more_info"].(map[string]interface{}); ok {
				if d, ok := mi["duration"].(string); ok {
					if di, err := strconv.Atoi(d); err == nil && di > 0 {
						dur = di
					}
				}
			}
			s := &Song{
				ID: sid, Title: title, Artist: artist, ArtistID: artistObj.ID,
				Album: albumObj.Name, AlbumID: albumObj.ID, CoverArt: albumObj.ID,
				Source: "jio", SourceID: id, ThumbURL: thumb, Duration: dur,
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
		if len(allSongs) >= 25 {
			break
		}
	}
	if len(allSongs) > 0 {
		go func() {
			_ = db.save()
			go telegramUploadDB()
		}()
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
	// saavn.sumit.co returns clear downloadUrl with 320kbps
	endpoints := []string{
		fmt.Sprintf("https://saavn.sumit.co/api/songs/%s", jioId),
		fmt.Sprintf("%s/songs?id=%s", strings.TrimRight(cfg.JioApiUrl, "/"), jioId),
	}
	for _, ep := range endpoints {
		resp, err := httpClient.Get(ep)
		if err != nil {
			log.Printf("JIO stream fail %s: %v", ep, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var g map[string]interface{}
		if err := json.Unmarshal(body, &g); err != nil {
			continue
		}
		var dlUrl string
		// {success:true, data:[{downloadUrl:[...]}]}
		if data, ok := g["data"].([]interface{}); ok {
			for _, it := range data {
				if m, ok := it.(map[string]interface{}); ok {
					if dl, ok := m["downloadUrl"]; ok {
						dlUrl = parseJioDownloadUrl(dl)
						if dlUrl != "" {
							break
						}
					}
					if dl, ok := m["download_url"]; ok {
						dlUrl = parseJioDownloadUrl(dl)
						if dlUrl != "" {
							break
						}
					}
				}
			}
		} else if data, ok := g["data"].(map[string]interface{}); ok {
			if songs, ok := data["songs"].([]interface{}); ok {
				for _, it := range songs {
					if m, ok := it.(map[string]interface{}); ok {
						if dl, ok := m["downloadUrl"]; ok {
							dlUrl = parseJioDownloadUrl(dl)
						}
					}
				}
			}
			if dlUrl == "" {
				if dl, ok := data["downloadUrl"]; ok {
					dlUrl = parseJioDownloadUrl(dl)
				}
			}
		}
		if dlUrl != "" {
			streamCache.Store("jio_"+jioId, cachedURL{URL: dlUrl, Expiry: time.Now().Add(time.Hour)})
			return dlUrl, nil
		}
	}
	return "", fmt.Errorf("jio stream not found for %s", jioId)
}

type YTSearch struct {
	Items []struct {
		ID struct {
			VideoID string `json:"videoId"`
		} `json:"id"`
		Snippet struct {
			Title        string `json:"title"`
			ChannelTitle string `json:"channelTitle"`
			Thumbnails   map[string]struct {
				URL string `json:"url"`
			} `json:"thumbnails"`
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
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("yt api %d: %s", resp.StatusCode, string(body))
	}
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
			if existing.PlayCount > 0 {
				s.PlayCount = existing.PlayCount
			}
		}
		db.Songs[s.ID] = s
		if !contains(al.SongIDs, s.ID) {
			al.SongIDs = append(al.SongIDs, s.ID)
		}
		db.Unlock()
	}
	go func() {
		_ = db.save()
		go telegramUploadDB()
	}()
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
		_ = os.WriteFile(cookiePath, []byte(cookieContent), 0600)
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
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "http") {
						streamCache.Store("yt_"+vid, cachedURL{URL: line, Expiry: time.Now().Add(30 * time.Minute)})
						return line, nil
					}
				}
			} else {
				log.Printf("YT attempt %d fail %s: %v stderr=%s", attempt, vid, err, errBuf.String())
			}
		case <-time.After(25 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			log.Printf("YT timeout %s attempt %d", vid, attempt)
		}
	}
	return "", fmt.Errorf("no url after retries")
}

// ---------- Audius (full free streams, no API key) ----------

func searchAudius(q string) ([]*Song, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	endpoints := []string{
		fmt.Sprintf("https://discoveryprovider.audius.co/v1/tracks/search?query=%s&app_name=substreamer", url.QueryEscape(q)),
		fmt.Sprintf("https://api.audius.co/v1/tracks/search?query=%s&app_name=substreamer", url.QueryEscape(q)),
	}
	var body []byte
	for _, ep := range endpoints {
		resp, err := httpClient.Get(ep)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 && len(b) > 10 {
			body = b
			break
		}
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("audius search failed")
	}
	var parsed struct {
		Data []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Duration    int    `json:"duration"`
			Permalink   string `json:"permalink"`
			Artwork     map[string]string `json:"artwork"`
			User        struct {
				Name string `json:"name"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	var songs []*Song
	for _, it := range parsed.Data {
		if it.ID == "" || it.Title == "" {
			continue
		}
		artist := it.User.Name
		if artist == "" {
			artist = "Audius"
		}
		thumb := ""
		for _, k := range []string{"480x480", "150x150", "1000x1000"} {
			if u, ok := it.Artwork[k]; ok && u != "" {
				thumb = u
				break
			}
		}
		dur := it.Duration
		if dur <= 0 {
			dur = 210
		}
		art := ensureArtist(artist)
		al := ensureAlbum(artist+" - Audius", artist, art.ID, thumb)
		s := &Song{
			ID: "audius_" + it.ID, Title: it.Title, Artist: artist, ArtistID: art.ID,
			Album: al.Name, AlbumID: al.ID, CoverArt: al.ID,
			Source: "audius", SourceID: it.ID, ThumbURL: thumb, Duration: dur,
		}
		songs = append(songs, s)
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
	if len(songs) > 0 {
		go func() { _ = db.save() }()
	}
	log.Printf("Audius search '%s' -> %d songs", q, len(songs))
	return songs, nil
}

func getAudiusStream(trackID string) (string, error) {
	cacheKey := "audius_" + trackID
	if v, ok := streamCache.Load(cacheKey); ok {
		c := v.(cachedURL)
		if time.Now().Before(c.Expiry) {
			return c.URL, nil
		}
	}
	// Audius stream endpoint redirects to CDN; use final URL
	endpoints := []string{
		fmt.Sprintf("https://discoveryprovider.audius.co/v1/tracks/%s/stream?app_name=substreamer", trackID),
		fmt.Sprintf("https://api.audius.co/v1/tracks/%s/stream?app_name=substreamer", trackID),
	}
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	for _, ep := range endpoints {
		req, _ := http.NewRequest("GET", ep, nil)
		req.Header.Set("User-Agent", "substreamer/5.3")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		// Prefer the final redirected URL if body is audio; otherwise use Location
		finalURL := resp.Request.URL.String()
		ct := resp.Header.Get("Content-Type")
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			if strings.HasPrefix(ct, "audio/") || strings.Contains(finalURL, "audius") || strings.HasPrefix(finalURL, "http") {
				streamCache.Store(cacheKey, cachedURL{URL: finalURL, Expiry: time.Now().Add(45 * time.Minute)})
				return finalURL, nil
			}
		}
		// Try HEAD-less: some nodes return 302 to media
		if finalURL != "" && finalURL != ep {
			streamCache.Store(cacheKey, cachedURL{URL: finalURL, Expiry: time.Now().Add(45 * time.Minute)})
			return finalURL, nil
		}
	}
	return "", fmt.Errorf("audius stream not found")
}

// ---------- Deezer (free search; 30s preview stream) ----------

func searchDeezer(q string) ([]*Song, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	ep := fmt.Sprintf("https://api.deezer.com/search?q=%s&limit=25", url.QueryEscape(q))
	resp, err := httpClient.Get(ep)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Data []struct {
			ID       int64  `json:"id"`
			Title    string `json:"title"`
			Duration int    `json:"duration"`
			Preview  string `json:"preview"`
			Artist   struct {
				Name string `json:"name"`
			} `json:"artist"`
			Album struct {
				Title string `json:"title"`
				Cover string `json:"cover_medium"`
				CoverXL string `json:"cover_xl"`
			} `json:"album"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	var songs []*Song
	for _, it := range parsed.Data {
		if it.ID == 0 || it.Title == "" {
			continue
		}
		artist := it.Artist.Name
		if artist == "" {
			artist = "Deezer"
		}
		thumb := it.Album.CoverXL
		if thumb == "" {
			thumb = it.Album.Cover
		}
		albumName := it.Album.Title
		if albumName == "" {
			albumName = "Deezer"
		}
		dur := it.Duration
		if dur <= 0 {
			dur = 30 // preview length
		}
		art := ensureArtist(artist)
		al := ensureAlbum(albumName, artist, art.ID, thumb)
		sid := fmt.Sprintf("deezer_%d", it.ID)
		s := &Song{
			ID: sid, Title: it.Title + " (preview)", Artist: artist, ArtistID: art.ID,
			Album: al.Name, AlbumID: al.ID, CoverArt: al.ID,
			Source: "deezer", SourceID: fmt.Sprintf("%d", it.ID),
			StreamURL: it.Preview, ThumbURL: thumb, Duration: dur,
		}
		songs = append(songs, s)
		db.Lock()
		db.Songs[s.ID] = s
		if !contains(al.SongIDs, s.ID) {
			al.SongIDs = append(al.SongIDs, s.ID)
		}
		db.Unlock()
	}
	if len(songs) > 0 {
		go func() { _ = db.save() }()
	}
	log.Printf("Deezer search '%s' -> %d songs", q, len(songs))
	return songs, nil
}

func getDeezerStream(trackID string, fallback string) (string, error) {
	if fallback != "" {
		return fallback, nil
	}
	// Re-fetch track for preview URL
	ep := fmt.Sprintf("https://api.deezer.com/track/%s", trackID)
	resp, err := httpClient.Get(ep)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var t struct {
		Preview string `json:"preview"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.Preview == "" {
		return "", fmt.Errorf("no deezer preview")
	}
	return t.Preview, nil
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
				Snippet struct {
					Title string `json:"title"`
				} `json:"snippet"`
			} `json:"items"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&pr)
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
					ResourceID struct {
						VideoID string `json:"videoId"`
					} `json:"resourceId"`
					Title        string `json:"title"`
					ChannelTitle string `json:"channelTitle"`
					Thumbnails   map[string]struct {
						URL string `json:"url"`
					} `json:"thumbnails"`
				} `json:"snippet"`
			} `json:"items"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&pr)
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
			s := &Song{
				ID: sID, Title: it.Snippet.Title, Artist: it.Snippet.ChannelTitle,
				ArtistID: art.ID, Album: al.Name, AlbumID: al.ID, CoverArt: al.ID,
				Source: "yt", SourceID: vid, ThumbURL: thumb, Duration: 210,
			}
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
	_ = db.save()
	go telegramUploadDB()
	return pl, nil
}

// --- Handlers ---

func handlePing(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{})
}

func handleLicense(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{"license": map[string]interface{}{"valid": true, "email": "admin@local", "licenseExpires": "2099-12-31T23:59:59"}})
}

func handleMusicFolders(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{"musicFolders": map[string]interface{}{"musicFolder": []map[string]interface{}{{"id": 0, "name": "Music"}}}})
}

func handleGetIndexes(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	db.RLock()
	defer db.RUnlock()
	idx := map[string][]map[string]interface{}{}
	for _, a := range db.Artists {
		letter := "#"
		if len(a.Name) > 0 {
			letter = strings.ToUpper(string(a.Name[0]))
			if letter < "A" || letter > "Z" {
				letter = "#"
			}
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
	user, ok := requireAuth(w, r)
	if !ok {
		return
	}
	respond(w, r, map[string]interface{}{
		"user": map[string]interface{}{
			"username": user, "adminRole": user == cfg.SubUser,
			"streamRole": true, "downloadRole": true, "playlistRole": true,
			"coverArtRole": true, "commentRole": true, "podcastRole": false,
			"shareRole": false, "jukeboxRole": false, "settingsRole": user == cfg.SubUser,
		},
	})
}

func handleGetArtists(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	db.RLock()
	defer db.RUnlock()
	idx := map[string][]map[string]interface{}{}
	for _, a := range db.Artists {
		letter := "#"
		if len(a.Name) > 0 {
			letter = strings.ToUpper(string(a.Name[0]))
			if letter < "A" || letter > "Z" {
				letter = "#"
			}
		}
		idx[letter] = append(idx[letter], map[string]interface{}{"id": a.ID, "name": a.Name, "albumCount": 1})
	}
	var indexes []map[string]interface{}
	for k, v := range idx {
		indexes = append(indexes, map[string]interface{}{"name": k, "artist": v})
	}
	respond(w, r, map[string]interface{}{"artists": map[string]interface{}{"index": indexes, "ignoredArticles": "The El La Los Las Le Les"}})
}

func handleGetArtist(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	id := r.URL.Query().Get("id")
	db.RLock()
	a, ok := db.Artists[id]
	if !ok {
		db.RUnlock()
		writeJSON(w, 200, subFail("Artist not found", 70))
		return
	}
	var albums []map[string]interface{}
	for _, al := range db.Albums {
		if al.ArtistID == id {
			albums = append(albums, map[string]interface{}{
				"id": al.ID, "name": al.Name, "songCount": len(al.SongIDs),
				"coverArt": al.ID, "artist": al.Artist, "artistId": al.ArtistID,
			})
		}
	}
	db.RUnlock()
	respond(w, r, map[string]interface{}{"artist": map[string]interface{}{"id": a.ID, "name": a.Name, "album": albums, "albumCount": len(albums)}})
}

func handleGetAlbum(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
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
		writeJSON(w, 200, subFail("Album not found", 70))
		return
	}
	var songs []map[string]interface{}
	for _, sid := range al.SongIDs {
		if s, ok := db.Songs[sid]; ok {
			songs = append(songs, songToSubsonic(s, r))
		}
	}
	db.RUnlock()
	respond(w, r, map[string]interface{}{
		"album": map[string]interface{}{
			"id": al.ID, "name": al.Name, "artist": al.Artist, "artistId": al.ArtistID,
			"coverArt": al.ID, "song": songs, "songCount": len(songs),
		},
	})
}

func handleGetSong(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	id := r.URL.Query().Get("id")
	db.RLock()
	s, ok := db.Songs[id]
	db.RUnlock()
	if !ok {
		writeJSON(w, 200, subFail("Song not found", 70))
		return
	}
	respond(w, r, map[string]interface{}{"song": songToSubsonic(s, r)})
}

func songToSubsonic(s *Song, r *http.Request) map[string]interface{} {
	sourceTag := ""
	switch s.Source {
	case "yt":
		sourceTag = "[YT] "
	case "jio":
		sourceTag = "[JIO] "
	case "audius":
		sourceTag = "[AU] "
	case "deezer":
		sourceTag = "[DZ] "
	}
	duration := s.Duration
	if duration <= 0 {
		duration = 210
	}
	m := map[string]interface{}{
		"id": s.ID, "parent": s.AlbumID, "title": sourceTag + s.Title,
		"artist": s.Artist, "album": s.Album, "albumId": s.AlbumID, "artistId": s.ArtistID,
		"coverArt": s.CoverArt, "duration": duration, "bitRate": 128,
		"contentType": "audio/mp4", "suffix": "m4a", "isDir": false, "type": "music",
		"genre": s.Source, "path": s.Source + "/" + s.ID + ".m4a",
		"playCount": s.PlayCount, "created": time.Now().Format(time.RFC3339),
	}
	if s.TgFileID != "" {
		m["comment"] = "cached"
	}
	return m
}

func handleSearch3(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	q := r.URL.Query().Get("query")
	if strings.TrimSpace(q) == "" {
		// Empty query: return local library page (Substreamer library sync path)
		sCount, _ := strconv.Atoi(r.URL.Query().Get("songCount"))
		aCount, _ := strconv.Atoi(r.URL.Query().Get("albumCount"))
		arCount, _ := strconv.Atoi(r.URL.Query().Get("artistCount"))
		sOff, _ := strconv.Atoi(r.URL.Query().Get("songOffset"))
		aOff, _ := strconv.Atoi(r.URL.Query().Get("albumOffset"))
		if sCount == 0 && aCount == 0 && arCount == 0 {
			respond(w, r, map[string]interface{}{"searchResult3": map[string]interface{}{
				"song": []interface{}{}, "album": []interface{}{}, "artist": []interface{}{},
			}})
			return
		}
		db.RLock()
		var songs []map[string]interface{}
		var albums []map[string]interface{}
		var artists []map[string]interface{}
		i := 0
		for _, s := range db.Songs {
			if sCount > 0 {
				if i >= sOff && len(songs) < sCount {
					songs = append(songs, songToSubsonic(s, r))
				}
				i++
			}
		}
		i = 0
		for _, al := range db.Albums {
			if aCount > 0 {
				if i >= aOff && len(albums) < aCount {
					albums = append(albums, map[string]interface{}{
						"id": al.ID, "name": al.Name, "artist": al.Artist, "artistId": al.ArtistID,
						"coverArt": al.ID, "songCount": len(al.SongIDs),
					})
				}
				i++
			}
		}
		i = 0
		for _, a := range db.Artists {
			if arCount > 0 && len(artists) < arCount {
				artists = append(artists, map[string]interface{}{"id": a.ID, "name": a.Name})
			}
			i++
		}
		db.RUnlock()
		respond(w, r, map[string]interface{}{"searchResult3": map[string]interface{}{
			"song": songs, "album": albums, "artist": artists,
		}})
		return
	}
	sCount, _ := strconv.Atoi(r.URL.Query().Get("songCount"))
	if sCount <= 0 {
		sCount = 50
	}
	if sCount > 100 {
		sCount = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("songOffset"))
	if offset < 0 {
		offset = 0
	}
	var jio, yt, audius, deezer []*Song
	var errJ, errY, errA, errD error
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); jio, errJ = searchJio(q) }()
	go func() { defer wg.Done(); yt, errY = searchYoutube(q) }()
	go func() { defer wg.Done(); audius, errA = searchAudius(q) }()
	go func() { defer wg.Done(); deezer, errD = searchDeezer(q) }()
	wg.Wait()
	if errJ != nil {
		log.Printf("searchJio err: %v", errJ)
	}
	if errY != nil {
		log.Printf("searchYoutube err: %v", errY)
	}
	if errA != nil {
		log.Printf("searchAudius err: %v", errA)
	}
	if errD != nil {
		log.Printf("searchDeezer err: %v", errD)
	}

	// Interleave sources for variety: Jio, YT, Audius, Deezer
	all := []*Song{}
	maxLen := len(jio)
	for _, L := range []int{len(yt), len(audius), len(deezer)} {
		if L > maxLen {
			maxLen = L
		}
	}
	for i := 0; i < maxLen; i++ {
		if i < len(jio) {
			all = append(all, jio[i])
		}
		if i < len(yt) {
			all = append(all, yt[i])
		}
		if i < len(audius) {
			all = append(all, audius[i])
		}
		if i < len(deezer) {
			all = append(all, deezer[i])
		}
	}
	if offset > 0 {
		if offset >= len(all) {
			all = []*Song{}
		} else {
			all = all[offset:]
		}
	}
	if len(all) > sCount {
		all = all[:sCount]
	}
	res := make([]map[string]interface{}, 0, len(all))
	for _, s := range all {
		res = append(res, songToSubsonic(s, r))
	}
	log.Printf("SEARCH '%s' -> JIO:%d YT:%d Audius:%d Deezer:%d Total:%d", q, len(jio), len(yt), len(audius), len(deezer), len(res))
	// Always use empty slices (never null) — Substreamer breaks on null
	respond(w, r, map[string]interface{}{
		"searchResult3": map[string]interface{}{
			"song":   res,
			"album":  []map[string]interface{}{},
			"artist": []map[string]interface{}{},
		},
	})
}

func handleAlbumList2(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 {
		size = 20
	}
	if size > 500 {
		size = 500
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	db.RLock()
	var albums []*Album
	for _, al := range db.Albums {
		albums = append(albums, al)
	}
	db.RUnlock()
	rand.Shuffle(len(albums), func(i, j int) { albums[i], albums[j] = albums[j], albums[i] })
	if offset >= len(albums) {
		albums = nil
	} else {
		albums = albums[offset:]
	}
	if len(albums) > size {
		albums = albums[:size]
	}
	var res []map[string]interface{}
	for _, al := range albums {
		res = append(res, map[string]interface{}{
			"id": al.ID, "name": al.Name, "artist": al.Artist, "artistId": al.ArtistID,
			"coverArt": al.ID, "songCount": len(al.SongIDs),
		})
	}
	respond(w, r, map[string]interface{}{"albumList2": map[string]interface{}{"album": res}})
}

func handleRandomSongs(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 {
		size = 20
	}
	db.RLock()
	var all []*Song
	for _, s := range db.Songs {
		all = append(all, s)
	}
	db.RUnlock()
	// FIXED: correct swap
	rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	if len(all) > size {
		all = all[:size]
	}
	var res []map[string]interface{}
	for _, s := range all {
		res = append(res, songToSubsonic(s, r))
	}
	respond(w, r, map[string]interface{}{"randomSongs": map[string]interface{}{"song": res}})
}

func findSongByID(id string) (*Song, string) {
	db.RLock()
	defer db.RUnlock()
	if s, ok := db.Songs[id]; ok {
		return s, id
	}
	for _, try := range []string{"yt_" + id, "jio_" + id, "audius_" + id, "deezer_" + id} {
		if s, ok := db.Songs[try]; ok {
			return s, try
		}
	}
	return nil, id
}

func handleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	log.Printf("STREAM REQ id=%s u=%s", r.URL.Query().Get("id"), r.URL.Query().Get("u"))
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", 400)
		return
	}
	s, resolvedID := findSongByID(id)
	id = resolvedID
	if s == nil {
		log.Printf("STREAM 404: id=%s not found, auto-creating", id)
		vid := id
		source := "yt"
		if strings.HasPrefix(id, "yt_") {
			vid = strings.TrimPrefix(id, "yt_")
			source = "yt"
		} else if strings.HasPrefix(id, "jio_") {
			vid = strings.TrimPrefix(id, "jio_")
			source = "jio"
		} else if len(id) >= 10 && !strings.Contains(id, "_") {
			vid = id
			source = "yt"
			id = "yt_" + vid
		}
		art := ensureArtist("YouTube")
		al := ensureAlbum("YouTube", "YouTube", art.ID, "")
		s = &Song{
			ID: id, Title: "Track " + vid, Artist: "YouTube", ArtistID: art.ID,
			Album: al.Name, AlbumID: al.ID, CoverArt: al.ID,
			Source: source, SourceID: vid, Duration: 210,
		}
		db.Lock()
		db.Songs[id] = s
		db.Unlock()
	}
	db.Lock()
	if song, ok := db.Songs[id]; ok {
		song.PlayCount++
	}
	db.Unlock()

	// Prefer Telegram cache (with Range support)
	if s.TgFileID != "" {
		if tgUrl, err := telegramGetFileUrl(s.TgFileID); err == nil {
			if proxyAudioWithRange(w, r, tgUrl, "TG") {
				log.Printf("STREAM TG HIT %s", id)
				return
			}
		}
	}

	var urlStr string
	var err error
	switch s.Source {
	case "jio":
		urlStr, err = getJioStream(s.SourceID)
		if (err != nil || urlStr == "") && s.StreamURL != "" {
			urlStr = s.StreamURL
			err = nil
		}
		if urlStr == "" {
			log.Printf("STREAM JIO no url for %s: %v", id, err)
			http.Error(w, "jio stream fail", 502)
			return
		}
	case "audius":
		urlStr, err = getAudiusStream(s.SourceID)
		if err != nil || urlStr == "" {
			log.Printf("STREAM Audius fail for %s: %v", id, err)
			http.Error(w, "audius stream fail", 502)
			return
		}
	case "deezer":
		urlStr, err = getDeezerStream(s.SourceID, s.StreamURL)
		if err != nil || urlStr == "" {
			log.Printf("STREAM Deezer fail for %s: %v", id, err)
			http.Error(w, "deezer preview fail", 502)
			return
		}
	default: // yt
		urlStr, err = getYoutubeStream(s.SourceID)
		if err != nil || urlStr == "" {
			log.Printf("STREAM YT fail for %s: %v", id, err)
			msg := "yt stream fail"
			if err != nil {
				msg += ": " + err.Error()
			}
			http.Error(w, msg, 502)
			return
		}
	}
	if s.TgFileID == "" && (s.Source == "yt" || s.Source == "audius") {
		go cacheSongToTelegram(s, urlStr)
	}
	if proxyAudioWithRange(w, r, urlStr, "MISS") {
		log.Printf("STREAM MISS OK %s range=%v", id, r.Header.Get("Range") != "")
		return
	}
	http.Error(w, "upstream fail", 502)
}

// proxyAudioWithRange forwards client Range headers to upstream and returns 206 when applicable.
func proxyAudioWithRange(w http.ResponseWriter, r *http.Request, urlStr, cacheTag string) bool {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	if ir := r.Header.Get("If-Range"); ir != "" {
		req.Header.Set("If-Range", ir)
	}
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("proxy upstream fail: %v", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		log.Printf("proxy upstream status %d: %s", resp.StatusCode, string(body))
		return false
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.HasPrefix(ct, "text/") {
		ct = "audio/mp4"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Accept-Ranges, Content-Length")
	w.Header().Set("X-Cache", cacheTag)
	w.Header().Set("Cache-Control", "no-cache")
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		w.Header().Set("Content-Range", cr)
	}
	if ar := resp.Header.Get("Accept-Ranges"); ar != "" {
		w.Header().Set("Accept-Ranges", ar)
	}
	w.WriteHeader(resp.StatusCode) // 200 or 206
	_, _ = io.Copy(w, resp.Body)
	return true
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	id := r.URL.Query().Get("id")
	s, id := findSongByID(id)
	if s == nil {
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
		var err error
		switch s.Source {
		case "jio":
			urlStr, _ = getJioStream(s.SourceID)
			if urlStr == "" {
				urlStr = s.StreamURL
			}
		case "audius":
			urlStr, err = getAudiusStream(s.SourceID)
		case "deezer":
			urlStr, err = getDeezerStream(s.SourceID, s.StreamURL)
		default:
			urlStr, err = getYoutubeStream(s.SourceID)
		}
		if err != nil || urlStr == "" {
			http.Error(w, "stream fail", 502)
			return
		}
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s - %s.m4a\"", s.Artist, s.Title))
	if !proxyAudioWithRange(w, r, urlStr, "DL") {
		http.Error(w, "upstream", 502)
	}
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
		if thumb == "" {
			if al, ok := db.Albums[s.AlbumID]; ok {
				thumb = al.ThumbURL
			}
		}
	}
	db.RUnlock()
	if thumb == "" {
		// 1x1 transparent pixel fallback instead of external redirect
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(200)
		// minimal 1x1 PNG
		_, _ = w.Write([]byte{
			0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
			0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
			0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
			0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
			0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
			0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
		})
		return
	}
	resp, err := httpClient.Get(thumb)
	if err != nil || resp == nil {
		http.Redirect(w, r, thumb, 302)
		return
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = io.Copy(w, resp.Body)
}

func handleGetPlaylists(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
		return
	}
	db.RLock()
	defer db.RUnlock()
	var list []map[string]interface{}
	for _, pl := range db.Playlists {
		if pl.Owner == user || pl.Public || user == cfg.SubUser {
			list = append(list, map[string]interface{}{
				"id": pl.ID, "name": pl.Name, "owner": pl.Owner,
				"songCount": len(pl.SongIDs), "created": pl.Created, "public": pl.Public,
			})
		}
	}
	respond(w, r, map[string]interface{}{"playlists": map[string]interface{}{"playlist": list}})
}

func handleGetPlaylist(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	id := r.URL.Query().Get("id")
	db.RLock()
	pl, ok := db.Playlists[id]
	if !ok {
		db.RUnlock()
		writeJSON(w, 200, subFail("Playlist not found", 70))
		return
	}
	var songs []map[string]interface{}
	for _, sid := range pl.SongIDs {
		if s, ok := db.Songs[sid]; ok {
			songs = append(songs, songToSubsonic(s, r))
		}
	}
	db.RUnlock()
	respond(w, r, map[string]interface{}{
		"playlist": map[string]interface{}{
			"id": pl.ID, "name": pl.Name, "owner": pl.Owner,
			"songCount": len(songs), "entry": songs, "created": pl.Created, "public": pl.Public,
		},
	})
}

func handleCreatePlaylist(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	playlistId := r.URL.Query().Get("playlistId")
	songIds := r.URL.Query()["songId"]
	if playlistId != "" {
		// Full replace existing playlist (Subsonic semantics)
		db.Lock()
		pl, exists := db.Playlists[playlistId]
		if !exists {
			db.Unlock()
			writeJSON(w, 200, subFail("Playlist not found", 70))
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
		pl.SongIDs = append([]string{}, songIds...)
		db.Unlock()
		_ = db.save()
		respond(w, r, map[string]interface{}{"playlist": map[string]interface{}{"id": pl.ID, "name": pl.Name}})
		return
	}
	if name == "" {
		name = "New Playlist"
	}
	id := "pl_" + fmt.Sprintf("%d", time.Now().UnixNano())
	pl := &Playlist{
		ID: id, Name: name, Owner: user, SongIDs: append([]string{}, songIds...),
		Created: time.Now().Format(time.RFC3339),
	}
	db.Lock()
	db.Playlists[id] = pl
	db.Unlock()
	_ = db.save()
	respond(w, r, map[string]interface{}{"playlist": map[string]interface{}{"id": pl.ID, "name": pl.Name}})
}

func handleUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("playlistId")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	name := r.URL.Query().Get("name")
	comment := r.URL.Query().Get("comment")
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
	if comment != "" {
		pl.Comment = comment
	}
	if pub := r.URL.Query().Get("public"); pub != "" {
		pl.Public = pub == "true" || pub == "1"
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
		// sort descending so removals don't shift indexes
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
	_ = db.save()
	go telegramUploadDB()
	respond(w, r, map[string]interface{}{})
}

func handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
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
	_ = db.save()
	respond(w, r, map[string]interface{}{})
}

func handleImportYT(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
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
	user, ok := requireAuth(w, r)
	if !ok {
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
	_ = db.save()
	go telegramUploadDB()
	respond(w, r, map[string]interface{}{"user": map[string]interface{}{"username": newU}})
}

func handleGetUsers(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
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
	user, ok := requireAuth(w, r)
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
	_ = db.save()
	respond(w, r, map[string]interface{}{})
}

func handleStar(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
		return
	}
	ids := r.URL.Query()["id"]
	albumIds := r.URL.Query()["albumId"]
	artistIds := r.URL.Query()["artistId"]
	db.Lock()
	if _, ok := db.Starred[user]; !ok {
		db.Starred[user] = make(map[string]bool)
	}
	for _, id := range ids {
		db.Starred[user][id] = true
	}
	for _, id := range albumIds {
		db.Starred[user]["album:"+id] = true
	}
	for _, id := range artistIds {
		db.Starred[user]["artist:"+id] = true
	}
	db.Unlock()
	_ = db.save()
	respond(w, r, map[string]interface{}{})
}

func handleUnstar(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
		return
	}
	ids := r.URL.Query()["id"]
	albumIds := r.URL.Query()["albumId"]
	artistIds := r.URL.Query()["artistId"]
	db.Lock()
	if m, ok := db.Starred[user]; ok {
		for _, id := range ids {
			delete(m, id)
		}
		for _, id := range albumIds {
			delete(m, "album:"+id)
		}
		for _, id := range artistIds {
			delete(m, "artist:"+id)
		}
	}
	db.Unlock()
	_ = db.save()
	respond(w, r, map[string]interface{}{})
}

func handleGetStarred(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
		return
	}
	db.RLock()
	starMap := db.Starred[user]
	var songs []map[string]interface{}
	var albums []map[string]interface{}
	var artists []map[string]interface{}
	for sid := range starMap {
		if strings.HasPrefix(sid, "album:") {
			aid := strings.TrimPrefix(sid, "album:")
			if al, ok := db.Albums[aid]; ok {
				albums = append(albums, map[string]interface{}{
					"id": al.ID, "name": al.Name, "artist": al.Artist, "coverArt": al.ID, "songCount": len(al.SongIDs),
				})
			}
			continue
		}
		if strings.HasPrefix(sid, "artist:") {
			aid := strings.TrimPrefix(sid, "artist:")
			if a, ok := db.Artists[aid]; ok {
				artists = append(artists, map[string]interface{}{"id": a.ID, "name": a.Name})
			}
			continue
		}
		if s, ok := db.Songs[sid]; ok {
			songs = append(songs, songToSubsonic(s, r))
		}
	}
	db.RUnlock()
	// Substreamer uses getStarred2 → starred2 key
	if strings.Contains(r.URL.Path, "getStarred2") {
		respond(w, r, map[string]interface{}{
			"starred2": map[string]interface{}{"song": songs, "album": albums, "artist": artists},
		})
		return
	}
	respond(w, r, map[string]interface{}{
		"starred": map[string]interface{}{"song": songs, "album": albums, "artist": artists},
	})
}

func handleSetRating(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	rating, _ := strconv.Atoi(r.URL.Query().Get("rating"))
	if id == "" {
		writeJSON(w, 200, subFail("id required", 10))
		return
	}
	db.Lock()
	if _, ok := db.Ratings[user]; !ok {
		db.Ratings[user] = make(map[string]int)
	}
	if rating <= 0 {
		delete(db.Ratings[user], id)
	} else {
		if rating > 5 {
			rating = 5
		}
		db.Ratings[user][id] = rating
	}
	db.Unlock()
	_ = db.save()
	respond(w, r, map[string]interface{}{})
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
	db.RLock()
	count := len(db.Songs)
	db.RUnlock()
	respond(w, r, map[string]interface{}{
		"scanStatus": map[string]interface{}{"scanning": false, "count": count},
	})
}

func handleStartScan(w http.ResponseWriter, r *http.Request) {
	db.RLock()
	count := len(db.Songs)
	db.RUnlock()
	respond(w, r, map[string]interface{}{
		"scanStatus": map[string]interface{}{"scanning": false, "count": count},
	})
}

func handleGetArtistInfo2(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	id := r.URL.Query().Get("id")
	db.RLock()
	a, ok := db.Artists[id]
	db.RUnlock()
	if !ok {
		respond(w, r, map[string]interface{}{"artistInfo2": map[string]interface{}{}})
		return
	}
	respond(w, r, map[string]interface{}{
		"artistInfo2": map[string]interface{}{
			"biography": "", "musicBrainzId": "", "lastFmUrl": "",
			"smallImageUrl": "", "mediumImageUrl": "", "largeImageUrl": "",
			"similarArtist": []interface{}{}, "name": a.Name,
		},
	})
}

func handleGetAlbumInfo2(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{
		"albumInfo": map[string]interface{}{
			"notes": "", "musicBrainzId": "", "lastFmUrl": "",
			"smallImageUrl": "", "mediumImageUrl": "", "largeImageUrl": "",
		},
	})
}

func handleGetSimilarSongs(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	// Return random songs as similar fallback
	handleRandomSongs(w, r)
}

func handleGetTopSongs(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	if count <= 0 {
		count = 20
	}
	artistName := r.URL.Query().Get("artist")
	db.RLock()
	var songs []*Song
	for _, s := range db.Songs {
		if artistName == "" || strings.EqualFold(s.Artist, artistName) {
			songs = append(songs, s)
		}
	}
	db.RUnlock()
	// sort by play count desc (simple bubble)
	for i := 0; i < len(songs); i++ {
		for j := i + 1; j < len(songs); j++ {
			if songs[j].PlayCount > songs[i].PlayCount {
				songs[i], songs[j] = songs[j], songs[i]
			}
		}
	}
	if len(songs) > count {
		songs = songs[:count]
	}
	var res []map[string]interface{}
	for _, s := range songs {
		res = append(res, songToSubsonic(s, r))
	}
	respond(w, r, map[string]interface{}{"topSongs": map[string]interface{}{"song": res}})
}

func handleGetLyrics(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{"lyrics": map[string]interface{}{"value": ""}})
}

func handleGetLyricsBySongId(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{"lyricsList": map[string]interface{}{"structuredLyrics": []interface{}{}}})
}

func handleGetGenres(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{
		"genres": map[string]interface{}{
			"genre": []map[string]interface{}{
				{"value": "yt", "songCount": 0, "albumCount": 0},
				{"value": "jio", "songCount": 0, "albumCount": 0},
			},
		},
	})
}

func handleGetSongsByGenre(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	genre := r.URL.Query().Get("genre")
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	if count <= 0 {
		count = 20
	}
	db.RLock()
	var res []map[string]interface{}
	for _, s := range db.Songs {
		if genre == "" || s.Source == genre {
			res = append(res, songToSubsonic(s, r))
			if len(res) >= count {
				break
			}
		}
	}
	db.RUnlock()
	respond(w, r, map[string]interface{}{"songsByGenre": map[string]interface{}{"song": res}})
}

func handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
		return
	}
	username := r.URL.Query().Get("username")
	password := r.URL.Query().Get("password")
	if username == "" {
		username = user
	}
	if username != user && user != cfg.SubUser {
		writeJSON(w, 200, subFail("not allowed", 50))
		return
	}
	if password == "" {
		writeJSON(w, 200, subFail("password required", 10))
		return
	}
	muUsers.Lock()
	usersMap[username] = password
	muUsers.Unlock()
	db.Lock()
	db.Users[username] = password
	db.Unlock()
	_ = db.save()
	respond(w, r, map[string]interface{}{})
}

// ---------- Missing Subsonic / OpenSubsonic endpoint handlers ----------

func handleGetMusicDirectory(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	db.RLock()
	defer db.RUnlock()

	if id == "" || id == "0" || id == "root" {
		var children []map[string]interface{}
		for _, a := range db.Artists {
			children = append(children, map[string]interface{}{
				"id": a.ID, "parent": "0", "title": a.Name, "artist": a.Name,
				"isDir": true, "coverArt": a.ID,
			})
		}
		respond(w, r, map[string]interface{}{
			"directory": map[string]interface{}{"id": "0", "name": "Music", "child": children},
		})
		return
	}

	if a, ok := db.Artists[id]; ok {
		var children []map[string]interface{}
		for _, al := range db.Albums {
			if al.ArtistID == id {
				children = append(children, map[string]interface{}{
					"id": al.ID, "parent": id, "title": al.Name, "artist": a.Name,
					"isDir": true, "coverArt": al.ID, "album": al.Name,
				})
			}
		}
		respond(w, r, map[string]interface{}{
			"directory": map[string]interface{}{"id": a.ID, "name": a.Name, "child": children},
		})
		return
	}

	if al, ok := db.Albums[id]; ok {
		var children []map[string]interface{}
		for _, sid := range al.SongIDs {
			if s, ok := db.Songs[sid]; ok {
				children = append(children, songToSubsonic(s, r))
			}
		}
		respond(w, r, map[string]interface{}{
			"directory": map[string]interface{}{"id": al.ID, "name": al.Name, "child": children},
		})
		return
	}

	writeJSON(w, 200, subFail("Directory not found", 70))
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	handleSearch3(w, r)
}

func handleGetAvatar(w http.ResponseWriter, r *http.Request) {
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
		0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
		0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(200)
	_, _ = w.Write(png)
}

func handleGetNowPlaying(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{
		"nowPlaying": map[string]interface{}{"entry": []interface{}{}},
	})
}

func handleHLS(w http.ResponseWriter, r *http.Request) {
	handleStream(w, r)
}

func handleGetCaptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, subFail("No captions", 70))
}

func handleTokenInfo(w http.ResponseWriter, r *http.Request) {
	ok, user := checkAuth(r)
	if !ok {
		writeJSON(w, 200, subFail("auth", 40))
		return
	}
	respond(w, r, map[string]interface{}{
		"tokenInfo": map[string]interface{}{"username": user},
	})
}

func handleCreateShare(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	ids := r.URL.Query()["id"]
	desc := r.URL.Query().Get("description")
	shareID := fmt.Sprintf("share_%d", time.Now().UnixNano())
	respond(w, r, map[string]interface{}{
		"shares": map[string]interface{}{
			"share": []map[string]interface{}{{
				"id": shareID, "url": "https://example.invalid/share/" + shareID,
				"description": desc, "username": r.URL.Query().Get("u"),
				"created": time.Now().Format(time.RFC3339), "visitCount": 0,
				"entry": ids,
			}},
		},
	})
}

func handleUpdateShare(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleCreateBookmark(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	user, ok := requireAuth(w, r)
	if !ok {
		return
	}
	if user != cfg.SubUser {
		writeJSON(w, 200, subFail("admin only", 50))
		return
	}
	username := r.URL.Query().Get("username")
	password := r.URL.Query().Get("password")
	if username != "" && password != "" {
		muUsers.Lock()
		usersMap[username] = password
		muUsers.Unlock()
		db.Lock()
		db.Users[username] = password
		db.Unlock()
		_ = db.save()
	}
	respond(w, r, map[string]interface{}{})
}

func handleJukeboxControl(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{
		"jukeboxStatus": map[string]interface{}{
			"currentIndex": -1, "playing": false, "gain": 0.5, "position": 0,
		},
	})
}

func handleAddChatMessage(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleCreateInternetRadioStation(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleUpdateInternetRadioStation(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleDeleteInternetRadioStation(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleGetVideos(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{"videos": map[string]interface{}{"video": []interface{}{}}})
}

func handleGetVideoInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, subFail("not found", 70))
}

func handleGetNewestPodcasts(w http.ResponseWriter, r *http.Request) {
	respond(w, r, map[string]interface{}{"newestPodcasts": map[string]interface{}{"episode": []interface{}{}}})
}

func handleRefreshPodcasts(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleCreatePodcastChannel(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleDeletePodcastChannel(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleDeletePodcastEpisode(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleDownloadPodcastEpisode(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
		return
	}
	respond(w, r, map[string]interface{}{})
}

func handleGetPodcastEpisode(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, subFail("not found", 70))
}

func handleEmpty(key string, inner string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{key: map[string]interface{}{inner: []interface{}{}}})
	}
}

func handleRestCatchAll(w http.ResponseWriter, r *http.Request) {
	log.Printf("CATCH-ALL: %s %s", r.Method, r.URL.Path)
	respond(w, r, map[string]interface{}{})
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
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
	pls := len(db.Playlists)
	db.RUnlock()
	respond(w, r, map[string]interface{}{
		"connections": status,
		"stats":       map[string]interface{}{"songs": songsCount, "cached": cachedCount, "playlists": pls, "users": len(usersMap)},
	})
}

func handleTgIndex(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAuth(w, r); !ok {
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

func register(mux *http.ServeMux, path string, h http.HandlerFunc) {
	mux.HandleFunc(path+".view", h)
	mux.HandleFunc(path, h)
}

func main() {
	cfg = loadConfig()
	log.Println("=== SUBSTREAMER-COMPATIBLE API v5.3 REPAIRED ===")
	log.Printf("YT_API_KEY len=%d cookies size=%d", len(cfg.YtApiKey), len(getCookiesContent()))

	if err := db.load(); err != nil {
		log.Printf("DB load: %v", err)
	}
	log.Printf("DB from file: %d songs", len(db.Songs))
	if len(db.Songs) == 0 && cfg.TelegramFileID != "" {
		log.Println("DB empty, trying download from TG...")
		if err := telegramDownloadDB(); err != nil {
			log.Printf("DB download failed: %v", err)
		} else {
			_ = db.load()
			log.Printf("DB restored from TG: %d songs", len(db.Songs))
		}
	}
	if len(db.Songs) == 0 {
		log.Println("WARNING: DB 0 songs — search still works via YT/JIO. Import playlist to fill library.")
	}
	_ = os.MkdirAll(filepath.Dir(cfg.DbPath), 0755)
	initUsers()
	if cc := getCookiesContent(); cc != "" {
		_ = os.WriteFile("/tmp/cookies.txt", []byte(cc), 0600)
	}

	mux := http.NewServeMux()

	register(mux, "/rest/ping", handlePing)
	register(mux, "/rest/getLicense", handleLicense)
	register(mux, "/rest/getMusicFolders", handleMusicFolders)
	register(mux, "/rest/getIndexes", handleGetIndexes)
	register(mux, "/rest/getUser", handleGetUser)
	register(mux, "/rest/getArtists", handleGetArtists)
	register(mux, "/rest/getArtist", handleGetArtist)
	register(mux, "/rest/getAlbum", handleGetAlbum)
	register(mux, "/rest/getSong", handleGetSong)
	register(mux, "/rest/search3", handleSearch3)
	register(mux, "/rest/search2", handleSearch3)
	register(mux, "/rest/getAlbumList", handleAlbumList2)
	register(mux, "/rest/getAlbumList2", handleAlbumList2)
	register(mux, "/rest/getRandomSongs", handleRandomSongs)
	register(mux, "/rest/stream", handleStream)
	register(mux, "/rest/download", handleDownload)
	register(mux, "/rest/getCoverArt", handleCoverArt)
	register(mux, "/rest/getPlaylists", handleGetPlaylists)
	register(mux, "/rest/getPlaylist", handleGetPlaylist)
	register(mux, "/rest/createPlaylist", handleCreatePlaylist)
	register(mux, "/rest/deletePlaylist", handleDeletePlaylist)
	register(mux, "/rest/updatePlaylist", handleUpdatePlaylist)
	register(mux, "/rest/importYoutubePlaylist", handleImportYT)
	register(mux, "/rest/createUser", handleCreateUser)
	register(mux, "/rest/getUsers", handleGetUsers)
	register(mux, "/rest/deleteUser", handleDeleteUser)
	register(mux, "/rest/star", handleStar)
	register(mux, "/rest/unstar", handleUnstar)
	register(mux, "/rest/getStarred", handleGetStarred)
	register(mux, "/rest/getStarred2", handleGetStarred)
	register(mux, "/rest/setRating", handleSetRating)
	register(mux, "/rest/scrobble", handleScrobble)
	register(mux, "/rest/getScanStatus", handleScanStatus)
	register(mux, "/rest/startScan", handleStartScan)
	register(mux, "/rest/getArtistInfo2", handleGetArtistInfo2)
	register(mux, "/rest/getArtistInfo", handleGetArtistInfo2)
	register(mux, "/rest/getAlbumInfo2", handleGetAlbumInfo2)
	register(mux, "/rest/getAlbumInfo", handleGetAlbumInfo2)
	register(mux, "/rest/getSimilarSongs", handleGetSimilarSongs)
	register(mux, "/rest/getSimilarSongs2", handleGetSimilarSongs)
	register(mux, "/rest/getTopSongs", handleGetTopSongs)
	register(mux, "/rest/getLyrics", handleGetLyrics)
	register(mux, "/rest/getLyricsBySongId", handleGetLyricsBySongId)
	register(mux, "/rest/getGenres", handleGetGenres)
	register(mux, "/rest/getSongsByGenre", handleGetSongsByGenre)
	register(mux, "/rest/changePassword", handleChangePassword)
	register(mux, "/rest/getConnections", handleConnections)
	register(mux, "/rest/tgIndex", handleTgIndex)

	register(mux, "/rest/getOpenSubsonicExtensions", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{
			"openSubsonicExtensions": []interface{}{
				map[string]interface{}{"name": "songLyrics", "versions": []int{1}},
				map[string]interface{}{"name": "formPost", "versions": []int{1}},
				map[string]interface{}{"name": "transcoding", "versions": []int{1}},
			},
		})
	})

	// Browsing (folder mode)
	register(mux, "/rest/getMusicDirectory", handleGetMusicDirectory)
	register(mux, "/rest/search", handleSearch)

	// Media extras
	register(mux, "/rest/getAvatar", handleGetAvatar)
	register(mux, "/rest/hls", handleHLS)
	register(mux, "/rest/getCaptions", handleGetCaptions)
	register(mux, "/rest/getNowPlaying", handleGetNowPlaying)
	register(mux, "/rest/getVideos", handleGetVideos)
	register(mux, "/rest/getVideoInfo", handleGetVideoInfo)

	// OpenSubsonic
	register(mux, "/rest/tokenInfo", handleTokenInfo)

	// Sharing
	register(mux, "/rest/getShares", handleEmpty("shares", "share"))
	register(mux, "/rest/createShare", handleCreateShare)
	register(mux, "/rest/updateShare", handleUpdateShare)
	register(mux, "/rest/deleteShare", handleDeleteShare)

	// Bookmarks
	register(mux, "/rest/getBookmarks", handleEmpty("bookmarks", "bookmark"))
	register(mux, "/rest/createBookmark", handleCreateBookmark)
	register(mux, "/rest/deleteBookmark", handleDeleteBookmark)

	// Play queue
	register(mux, "/rest/getPlayQueue", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{"playQueue": map[string]interface{}{"entry": []interface{}{}, "current": 0, "position": 0}})
	})
	register(mux, "/rest/savePlayQueue", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, map[string]interface{}{})
	})

	// Users
	register(mux, "/rest/updateUser", handleUpdateUser)

	// Jukebox / Chat / Radio
	register(mux, "/rest/jukeboxControl", handleJukeboxControl)
	register(mux, "/rest/getChatMessages", handleEmpty("chatMessages", "chatMessage"))
	register(mux, "/rest/addChatMessage", handleAddChatMessage)
	register(mux, "/rest/getInternetRadioStations", handleEmpty("internetRadioStations", "internetRadioStation"))
	register(mux, "/rest/createInternetRadioStation", handleCreateInternetRadioStation)
	register(mux, "/rest/updateInternetRadioStation", handleUpdateInternetRadioStation)
	register(mux, "/rest/deleteInternetRadioStation", handleDeleteInternetRadioStation)

	// Podcasts
	register(mux, "/rest/getPodcasts", handleEmpty("podcasts", "channel"))
	register(mux, "/rest/getNewestPodcasts", handleGetNewestPodcasts)
	register(mux, "/rest/refreshPodcasts", handleRefreshPodcasts)
	register(mux, "/rest/createPodcastChannel", handleCreatePodcastChannel)
	register(mux, "/rest/deletePodcastChannel", handleDeletePodcastChannel)
	register(mux, "/rest/deletePodcastEpisode", handleDeletePodcastEpisode)
	register(mux, "/rest/downloadPodcastEpisode", handleDownloadPodcastEpisode)
	register(mux, "/rest/getPodcastEpisode", handleGetPodcastEpisode)

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
		db.RUnlock()
		status := getConnectionsFast()
		writeJSON(w, 200, map[string]interface{}{
			"status": "ok", "songs": songsCount, "playlists": plsCount,
			"users": len(usersMap), "cached": cached, "connections": status,
			"version": "5.3-repaired",
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.URL.Path != "/" {
			log.Printf("UNKNOWN 404: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<html><body style="background:#111;color:#eee;font-family:monospace;padding:24px">
<h2>Subsonic API v5.3 (Substreamer compatible)</h2>
<p>Search: /rest/search3.view?u=admin&amp;p=PASS&amp;v=1.16.1&amp;c=substreamer&amp;f=json&amp;query=kesariya</p>
<p>Import YT playlist: /rest/importYoutubePlaylist.view?u=admin&amp;p=PASS&amp;v=1.16.1&amp;c=substreamer&amp;f=json&amp;url=PLAYLIST_URL</p>
<p>Health: /health</p>
</body></html>`)
	})

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			_ = db.save()
			go telegramUploadDB()
		}
	}()

	port := cfg.Port
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	log.Printf("Starting on %s", port)
	log.Fatal(http.ListenAndServe(port, corsMiddleware(mux)))
}
