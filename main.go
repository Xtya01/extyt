package main

import (
	"bytes"
	"context"
	"crypto/des"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// -------------------- Models --------------------

type Song struct {
	YTID       string `json:"yt_id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	FileID     string `json:"file_id"`
	FilePath   string `json:"file_path"`
	AddedAt    string `json:"added_at"`
	PlayCount  int    `json:"play_count"`
	LastPlayed string `json:"last_played"`
	Starred    bool   `json:"starred"`
	Rating     int    `json:"rating"`
	Duration   int    `json:"duration"`
	CoverArt   string `json:"cover_art,omitempty"`
	Lyrics     string `json:"lyrics,omitempty"`
}

type Playlist struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	SongIDs []string `json:"song_ids"`
	Created string   `json:"created"`
	Changed string   `json:"changed"`
	Owner   string   `json:"owner"`
	Comment string   `json:"comment"`
	Public  bool     `json:"public"`
	YTURL   string   `json:"yt_url,omitempty"`
}

type User struct {
	Username   string            `json:"username"`
	Password   string            `json:"password"`
	Starred    map[string]bool   `json:"starred"`
	PlayCount  map[string]int    `json:"play_count"`
	LastPlayed map[string]string `json:"last_played"`
}

type AppDB struct {
	Songs     map[string]Song     `json:"songs"`
	Playlists map[string]Playlist `json:"playlists"`
	Users     map[string]User     `json:"users"`
}

var (
	appDB = AppDB{
		Songs:     make(map[string]Song),
		Playlists: make(map[string]Playlist),
		Users:     make(map[string]User),
	}
	mu           sync.RWMutex
	backupLock   sync.Mutex
	cookieOnce   sync.Once
	hasCookies   bool
	latestFileID string
	searchCache  sync.Map
	sem          = make(chan struct{}, 1) // Prevents RAM exhaustion on Koyeb Free (512MB)
)

const dbPath = "/tmp/db.json"
const cookiePath = "/tmp/cookies.txt"

// -------------------- Database Parser & Telegram Persistence --------------------

func parseDBData(data []byte) (AppDB, bool) {
	var newDB AppDB
	if err := json.Unmarshal(data, &newDB); err == nil && len(newDB.Songs) > 0 {
		if newDB.Playlists == nil {
			newDB.Playlists = make(map[string]Playlist)
		}
		if newDB.Users == nil {
			newDB.Users = make(map[string]User)
		}
		return newDB, true
	}

	var oldMap map[string]Song
	if err := json.Unmarshal(data, &oldMap); err == nil && len(oldMap) > 0 {
		return AppDB{
			Songs:     oldMap,
			Playlists: make(map[string]Playlist),
			Users:     make(map[string]User),
		}, true
	}

	if newDB.Songs != nil {
		if newDB.Playlists == nil {
			newDB.Playlists = make(map[string]Playlist)
		}
		if newDB.Users == nil {
			newDB.Users = make(map[string]User)
		}
		return newDB, true
	}

	return AppDB{}, false
}

func loadDB() {
	mu.Lock()
	defer mu.Unlock()

	loaded := false

	if data, err := os.ReadFile(dbPath); err == nil {
		if parsed, ok := parseDBData(data); ok && len(parsed.Songs) > 0 {
			appDB = parsed
			loaded = true
			log.Printf("[DB] Loaded local cache: %d songs, %d playlists, %d users", len(appDB.Songs), len(appDB.Playlists), len(appDB.Users))
		}
	}

	if !loaded || len(appDB.Songs) == 0 {
		fileID := strings.TrimSpace(os.Getenv("DB_JSON_FILE_ID"))
		if fileID != "" {
			log.Printf("[DB] Fetching database from Telegram FileID: %s...", fileID)
			data, err := downloadDBFromTelegram(fileID)
			if err == nil {
				if parsed, ok := parseDBData(data); ok && len(parsed.Songs) > 0 {
					appDB = parsed
					latestFileID = fileID
					os.WriteFile(dbPath, data, 0644)
					log.Printf("[DB] Successfully restored from Telegram: %d songs, %d playlists, %d users", len(appDB.Songs), len(appDB.Playlists), len(appDB.Users))
					ensureAdminUser()
					return
				}
			} else {
				log.Printf("[DB] Telegram download error: %v", err)
			}
		}
	}

	if len(appDB.Songs) == 0 {
		appDB = AppDB{
			Songs:     make(map[string]Song),
			Playlists: make(map[string]Playlist),
			Users:     make(map[string]User),
		}
		log.Println("[DB] Initialized fresh empty DB")
	}

	ensureAdminUser()
}

func ensureAdminUser() {
	adminU, adminP := getAdminCreds()
	if _, ok := appDB.Users[adminU]; !ok {
		appDB.Users[adminU] = User{
			Username:   adminU,
			Password:   adminP,
			Starred:    make(map[string]bool),
			PlayCount:  make(map[string]int),
			LastPlayed: make(map[string]string),
		}
	}
}

func saveDB() {
	mu.RLock()
	data, err := json.MarshalIndent(appDB, "", "  ")
	mu.RUnlock()
	if err != nil {
		return
	}
	os.WriteFile(dbPath, data, 0644)
	go backupDBToTelegram()
}

func backupDBToTelegram() {
	if !backupLock.TryLock() {
		return
	}
	defer backupLock.Unlock()

	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	chatID := strings.TrimSpace(os.Getenv("CHANNEL_ID"))
	if token == "" || chatID == "" {
		return
	}

	mu.RLock()
	songCnt := len(appDB.Songs)
	plCnt := len(appDB.Playlists)
	userCnt := len(appDB.Users)
	mu.RUnlock()

	file, err := os.Open(dbPath)
	if err != nil {
		return
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("document", "db.json")
	if err != nil {
		return
	}
	io.Copy(part, file)
	writer.WriteField("chat_id", chatID)
	writer.WriteField("caption", fmt.Sprintf("DB Backup %s | Songs: %d | Playlists: %d | Users: %d", time.Now().Format("2006-01-02 15:04:05"), songCnt, plCnt, userCnt))
	writer.Close()

	req, _ := http.NewRequest("POST", fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", token), body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var res struct {
		Ok     bool `json:"ok"`
		Result struct {
			Document struct {
				FileID string `json:"file_id"`
			} `json:"document"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&res) == nil && res.Ok {
		latestFileID = res.Result.Document.FileID
		log.Printf("[Telegram Backup] Synced! FileID: %s", latestFileID)
	}
}

func downloadDBFromTelegram(fileID string) ([]byte, error) {
	fileID = strings.TrimSpace(fileID)
	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" || fileID == "" {
		return nil, fmt.Errorf("credentials missing")
	}

	client := &http.Client{Timeout: 25 * time.Second}
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", token, fileID)
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	var gf struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &gf); err != nil || !gf.Ok {
		return nil, fmt.Errorf("getFile error: %s", gf.Description)
	}

	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, gf.Result.FilePath)
	r2, err := client.Get(fileURL)
	if err != nil {
		return nil, err
	}
	defer r2.Body.Close()

	return io.ReadAll(r2.Body)
}

func getTelegramFilePath(fileID string) string {
	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	fileID = strings.TrimSpace(fileID)
	if token == "" || fileID == "" {
		return ""
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", token, fileID))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var res struct {
		Ok     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&res) == nil && res.Ok {
		return res.Result.FilePath
	}
	return ""
}

// -------------------- Secure Audio Proxy --------------------

func proxyTelegramAudio(w http.ResponseWriter, r *http.Request, token, fp string) {
	audioURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, fp)
	req, err := http.NewRequestWithContext(r.Context(), "GET", audioURL, nil)
	if err != nil {
		http.Error(w, "Proxy request error", 500)
		return
	}

	if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Storage unreachable", 502)
		return
	}
	defer resp.Body.Close()

	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if val := resp.Header.Get(h); val != "" {
			w.Header().Set(h, val)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// -------------------- Helpers & Cleaner --------------------

func initCookies() {
	cookieOnce.Do(func() {
		if b64 := strings.TrimSpace(os.Getenv("YT_COOKIES_B64")); len(b64) > 50 {
			b64 = strings.ReplaceAll(b64, "\n", "")
			b64 = strings.ReplaceAll(b64, "\r", "")
			b64 = strings.ReplaceAll(b64, " ", "")
			decoded, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				decoded, err = base64.RawStdEncoding.DecodeString(b64)
			}
			if err == nil && len(decoded) > 50 {
				os.WriteFile(cookiePath, decoded, 0644)
				hasCookies = true
				log.Println("[Cookies] Loaded successfully from YT_COOKIES_B64")
				return
			}
		}
		if raw := os.Getenv("YT_COOKIES"); len(raw) > 50 {
			os.WriteFile(cookiePath, []byte(raw), 0644)
			hasCookies = true
			log.Println("[Cookies] Loaded successfully from YT_COOKIES")
		}
	})
}

func getCookiesArg() []string {
	initCookies()
	if hasCookies {
		return []string{"--cookies", cookiePath}
	}
	return []string{}
}

func startTempCleaner() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			files, _ := filepath.Glob("/tmp/*.m4a")
			now := time.Now()
			for _, f := range files {
				if info, err := os.Stat(f); err == nil {
					if now.Sub(info.ModTime()) > 30*time.Minute {
						os.Remove(f)
					}
				}
			}
		}
	}()
}

// -------------------- Authentication --------------------

func getAdminCreds() (string, string) {
	u := strings.TrimSpace(os.Getenv("ADMIN_USER"))
	p := os.Getenv("ADMIN_PASS")
	if u == "" {
		u = "admin"
	}
	if p == "" {
		p = "admin"
	}
	return u, p
}

func verifyPassword(actualPassword, p, t, s string) bool {
	if p != "" {
		if strings.HasPrefix(p, "enc:") {
			hexPart := strings.TrimPrefix(p, "enc:")
			decoded, err := hex.DecodeString(hexPart)
			if err == nil && string(decoded) == actualPassword {
				return true
			}
			return false
		}
		return p == actualPassword
	}
	if t != "" && s != "" {
		hash := md5.Sum([]byte(actualPassword + s))
		expected := hex.EncodeToString(hash[:])
		return strings.EqualFold(t, expected)
	}
	return false
}

func getCurrentUser(r *http.Request) string {
	u := strings.TrimSpace(r.URL.Query().Get("u"))
	p := r.URL.Query().Get("p")
	t := r.URL.Query().Get("t")
	s := r.URL.Query().Get("s")
	if u == "" {
		return ""
	}

	adminUser, adminPass := getAdminCreds()
	if u == adminUser && verifyPassword(adminPass, p, t, s) {
		return u
	}

	mu.RLock()
	user, exists := appDB.Users[u]
	mu.RUnlock()
	if exists && verifyPassword(user.Password, p, t, s) {
		return u
	}
	return ""
}

func checkAuth(r *http.Request) bool {
	return getCurrentUser(r) != ""
}

func isAdmin(r *http.Request) bool {
	adminUser, _ := getAdminCreds()
	return getCurrentUser(r) == adminUser
}

// -------------------- Subsonic Response Encoders --------------------

func writeSubsonicError(w http.ResponseWriter, code int, msg string, f string) {
	if f == "json" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"subsonic-response":{"status":"failed","version":"1.16.1","error":{"code":%d,"message":"%s"}}}`, code, msg)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<subsonic-response status="failed" version="1.16.1" xmlns="http://subsonic.org/restapi">
  <error code="%d" message="%s"/>
</subsonic-response>`, code, msg)
}

func writeSubsonicOK(w http.ResponseWriter, f string, body interface{}) {
	if f == "json" {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"subsonic-response": map[string]interface{}{
				"status":  "ok",
				"version": "1.16.1",
			},
		}
		if m, ok := body.(map[string]interface{}); ok {
			for k, v := range m {
				resp["subsonic-response"].(map[string]interface{})[k] = v
			}
		}
		json.NewEncoder(w).Encode(resp)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprint(w, `<subsonic-response status="ok" version="1.16.1" xmlns="http://subsonic.org/restapi">`)
	if s, ok := body.(string); ok {
		fmt.Fprint(w, s)
	}
	fmt.Fprint(w, `</subsonic-response>`)
}

func songToMap(s Song, u *User) map[string]interface{} {
	albumName := "YouTube"
	if strings.HasPrefix(s.YTID, "js-") {
		albumName = "JioSaavn"
	}

	isStarred := false
	playCount := s.PlayCount
	lastPlayed := s.LastPlayed

	if u != nil {
		if u.Starred != nil && u.Starred[s.YTID] {
			isStarred = true
		}
		if pc, ok := u.PlayCount[s.YTID]; ok && pc > 0 {
			playCount = pc
		}
		if lp, ok := u.LastPlayed[s.YTID]; ok && lp != "" {
			lastPlayed = lp
		}
	}

	m := map[string]interface{}{
		"id":          s.YTID,
		"title":       s.Title,
		"album":       albumName,
		"albumId":     "al-" + s.YTID,
		"artist":      s.Artist,
		"artistId":    "ar-" + s.YTID,
		"coverArt":    s.YTID,
		"duration":    s.Duration,
		"bitRate":     320,
		"suffix":      "m4a",
		"contentType": "audio/mp4",
		"isDir":       false,
		"playCount":   playCount,
		"created":     s.AddedAt,
		"userRating":  s.Rating,
	}

	if isStarred {
		if lastPlayed != "" {
			m["starred"] = lastPlayed
		} else {
			m["starred"] = s.AddedAt
		}
	}
	if lastPlayed != "" {
		m["played"] = lastPlayed
	}
	if s.Duration == 0 {
		m["duration"] = 180
	}
	return m
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// -------------------- YouTube Engine (Search & Title) --------------------

func getYTTitle(ytUrl string, cookieArgs []string) (title, artist string) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	args := []string{
		"--print", "%(title)s|||%(artist)s|||%(uploader)s",
		"--no-download",
		"--no-playlist",
		"--no-check-certificate",
		"--no-warnings",
		"--geo-bypass",
		"--extractor-args", "youtube:player_client=android,web",
	}
	if len(cookieArgs) > 0 {
		args = append(args, cookieArgs...)
	}
	args = append(args, ytUrl)

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || strings.HasPrefix(l, "WARNING") || strings.HasPrefix(l, "ERROR") {
			continue
		}
		parts := strings.Split(l, "|||")
		if len(parts) >= 1 {
			title = strings.TrimSpace(parts[0])
		}
		if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" && strings.TrimSpace(parts[1]) != "NA" {
			artist = strings.TrimSpace(parts[1])
		} else if len(parts) >= 3 {
			artist = strings.TrimSpace(parts[2])
		}
		break
	}
	return title, artist
}

func searchYouTubeAPI(query string, maxResults int, apiKey string) []Song {
	apiURL := fmt.Sprintf("https://www.googleapis.com/youtube/v3/search?part=snippet&type=video&maxResults=%d&q=%s&key=%s",
		maxResults, url.QueryEscape(query), apiKey)
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		log.Printf("[YT API] Request error: %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[YT API] Non-200 response: %d", resp.StatusCode)
		return nil
	}

	var res struct {
		Items []struct {
			ID struct {
				VideoID string `json:"videoId"`
			} `json:"id"`
			Snippet struct {
				Title        string `json:"title"`
				ChannelTitle string `json:"channelTitle"`
			} `json:"snippet"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil
	}

	var results []Song
	now := time.Now().Format(time.RFC3339)
	for _, item := range res.Items {
		if item.ID.VideoID == "" {
			continue
		}
		title := html.UnescapeString(item.Snippet.Title)
		artist := html.UnescapeString(item.Snippet.ChannelTitle)
		if artist == "" {
			artist = "YouTube"
		}
		results = append(results, Song{
			YTID:     item.ID.VideoID,
			Title:    title + " [YT]",
			Artist:   artist,
			AddedAt:  now,
			Duration: 180,
			CoverArt: fmt.Sprintf("https://i.ytimg.com/vi/%s/hqdefault.jpg", item.ID.VideoID),
		})
	}
	return results
}

func searchYouTubeScraper(query string, maxResults int) []Song {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cookieArgs := getCookiesArg()
	searchTerm := fmt.Sprintf("ytsearch%d:%s", maxResults, query)

	args := []string{
		"--flat-playlist",
		"--print", "%(id)s|||%(title)s|||%(uploader)s",
		"--no-download",
		"--no-check-certificate",
		"--no-warnings",
		"--geo-bypass",
	}
	if len(cookieArgs) > 0 {
		args = append(args, cookieArgs...)
	}
	args = append(args, searchTerm)

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[YT Scraper] Error: %v | Log: %s", err, string(out))
		return nil
	}

	var results []Song
	now := time.Now().Format(time.RFC3339)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "WARNING") || strings.HasPrefix(line, "ERROR") {
			continue
		}
		parts := strings.SplitN(line, "|||", 3)
		if len(parts) < 2 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		title := strings.TrimSpace(parts[1])
		artist := "YouTube"
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
			artist = strings.TrimSpace(parts[2])
		}
		if id == "" || title == "" {
			continue
		}
		results = append(results, Song{
			YTID:     id,
			Title:    title + " [YT]",
			Artist:   artist,
			AddedAt:  now,
			Duration: 180,
			CoverArt: fmt.Sprintf("https://i.ytimg.com/vi/%s/hqdefault.jpg", id),
		})
	}
	return results
}

func searchYouTube(query string, maxResults int) []Song {
	if maxResults <= 0 {
		maxResults = 6
	}

	// 1. Official YouTube Data API
	if apiKey := strings.TrimSpace(os.Getenv("YOUTUBE_API_KEY")); apiKey != "" {
		if songs := searchYouTubeAPI(query, maxResults, apiKey); len(songs) > 0 {
			return songs
		}
	}

	// 2. Fallback: Fast flat playlist yt-dlp scraper
	return searchYouTubeScraper(query, maxResults)
}

// -------------------- Subsonic API Endpoints --------------------

func subsonicHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/rest/")
	path = strings.TrimSuffix(path, ".view")
	path = strings.ToLower(path)
	f := r.URL.Query().Get("f")
	if f == "" {
		f = "xml"
	}

	if !checkAuth(r) && path != "ping" {
		writeSubsonicError(w, 40, "Wrong username or password", f)
		return
	}

	currentUser := getCurrentUser(r)
	mu.RLock()
	userObj, hasUser := appDB.Users[currentUser]
	mu.RUnlock()
	var currentUserPtr *User
	if hasUser {
		currentUserPtr = &userObj
	}

	switch path {
	case "ping":
		writeSubsonicOK(w, f, map[string]interface{}{})

	case "getlicense":
		writeSubsonicOK(w, f, map[string]interface{}{
			"license": map[string]interface{}{
				"valid":          true,
				"email":          currentUser + "@local",
				"licenseExpires": "2099-01-01T00:00:00",
			},
		})

	case "getmusicfolders":
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"musicFolders": map[string]interface{}{
					"musicFolder": []map[string]interface{}{{"id": 1, "name": "Cloud Library"}},
				},
			})
		} else {
			writeSubsonicOK(w, f, `<musicFolders><musicFolder id="1" name="Cloud Library"/></musicFolders>`)
		}

	case "getgenres":
		genres := []map[string]interface{}{
			{"value": "Hindi", "songCount": len(appDB.Songs), "albumCount": 1},
			{"value": "Punjabi", "songCount": len(appDB.Songs), "albumCount": 1},
			{"value": "Pop", "songCount": len(appDB.Songs), "albumCount": 1},
		}
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"genres": map[string]interface{}{"genre": genres},
			})
		} else {
			writeSubsonicOK(w, f, `<genres><genre value="Hindi"/><genre value="Punjabi"/></genres>`)
		}

	case "getpodcasts":
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"podcasts": map[string]interface{}{"channel": []interface{}{}},
			})
		} else {
			writeSubsonicOK(w, f, `<podcasts/>`)
		}

	case "getindexes", "getartists":
		mu.RLock()
		artistMap := make(map[string]int)
		for _, s := range appDB.Songs {
			a := s.Artist
			if a == "" {
				a = "Music"
			}
			artistMap[a]++
		}
		mu.RUnlock()

		letterMap := make(map[string][]map[string]interface{})
		for name, cnt := range artistMap {
			letter := strings.ToUpper(string(name[0]))
			if letter < "A" || letter > "Z" {
				letter = "#"
			}
			id := "ar-" + strings.ToLower(strings.ReplaceAll(name, " ", "-"))
			letterMap[letter] = append(letterMap[letter], map[string]interface{}{
				"id": id, "name": name, "albumCount": 1, "songCount": cnt, "coverArt": id,
			})
		}

		var letters []string
		for l := range letterMap {
			letters = append(letters, l)
		}
		sort.Strings(letters)

		indexes := []map[string]interface{}{}
		for _, l := range letters {
			indexes = append(indexes, map[string]interface{}{"name": l, "artist": letterMap[l]})
		}

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"artists": map[string]interface{}{"index": indexes},
				"indexes": map[string]interface{}{"index": indexes, "lastModified": time.Now().UnixMilli()},
			})
		} else {
			var b strings.Builder
			b.WriteString(fmt.Sprintf(`<indexes lastModified="%d">`, time.Now().UnixMilli()))
			for _, l := range letters {
				b.WriteString(fmt.Sprintf(`<index name="%s">`, l))
				for _, a := range letterMap[l] {
					b.WriteString(fmt.Sprintf(`<artist id="%v" name="%v"/>`, a["id"], xmlEscape(fmt.Sprint(a["name"]))))
				}
				b.WriteString(`</index>`)
			}
			b.WriteString(`</indexes>`)
			writeSubsonicOK(w, f, b.String())
		}

	case "getartist":
		mu.RLock()
		songCount := len(appDB.Songs)
		mu.RUnlock()

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"artist": map[string]interface{}{
					"id": "ar-1", "name": "Music", "albumCount": 1,
					"album": []map[string]interface{}{{
						"id": "al-1", "name": "Cached Songs", "artist": "Music",
						"artistId": "ar-1", "songCount": songCount, "coverArt": "al-1",
					}},
				},
			})
		} else {
			writeSubsonicOK(w, f, fmt.Sprintf(`<artist id="ar-1" name="Music" albumCount="1"><album id="al-1" name="Cached Songs" artist="Music" artistId="ar-1" songCount="%d"/></artist>`, songCount))
		}

	case "getalbum", "getmusicdirectory":
		mu.RLock()
		allSongs := make([]Song, 0, len(appDB.Songs))
		for _, s := range appDB.Songs {
			allSongs = append(allSongs, s)
		}
		mu.RUnlock()

		children := make([]map[string]interface{}, 0, len(allSongs))
		for _, s := range allSongs {
			children = append(children, songToMap(s, currentUserPtr))
		}

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"album": map[string]interface{}{
					"id": "al-1", "name": "Cached Songs", "artist": "Music",
					"artistId": "ar-1", "songCount": len(allSongs), "coverArt": "al-1", "song": children,
				},
				"directory": map[string]interface{}{
					"id": "al-1", "name": "Cached Songs", "child": children,
				},
			})
		} else {
			var b strings.Builder
			b.WriteString(fmt.Sprintf(`<album id="al-1" name="Cached Songs" artist="YouTube" artistId="ar-1" songCount="%d">`, len(allSongs)))
			for _, s := range allSongs {
				b.WriteString(fmt.Sprintf(`<song id="%s" title="%s" artist="%s" coverArt="%s" duration="%d" playCount="%d"/>`, s.YTID, xmlEscape(s.Title), xmlEscape(s.Artist), s.YTID, s.Duration, s.PlayCount))
			}
			b.WriteString(`</album>`)
			writeSubsonicOK(w, f, b.String())
		}

	case "getsong":
		id := r.URL.Query().Get("id")
		mu.RLock()
		song, ok := appDB.Songs[id]
		mu.RUnlock()
		if !ok {
			writeSubsonicError(w, 70, "Song not found", f)
			return
		}
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"song": songToMap(song, currentUserPtr)})
		} else {
			writeSubsonicOK(w, f, fmt.Sprintf(`<song id="%s" title="%s" artist="%s" coverArt="%s" duration="%d" playCount="%d"/>`, song.YTID, xmlEscape(song.Title), xmlEscape(song.Artist), song.YTID, song.Duration, song.PlayCount))
		}

	case "stream", "download":
		id := r.URL.Query().Get("id")
		if id == "" {
			writeSubsonicError(w, 10, "Missing id", f)
			return
		}

		go func(sid, uname string) {
			mu.Lock()
			now := time.Now().Format(time.RFC3339)
			if s, ok := appDB.Songs[sid]; ok {
				s.PlayCount++
				s.LastPlayed = now
				appDB.Songs[sid] = s
			}
			if u, ok := appDB.Users[uname]; ok {
				if u.PlayCount == nil {
					u.PlayCount = make(map[string]int)
				}
				if u.LastPlayed == nil {
					u.LastPlayed = make(map[string]string)
				}
				u.PlayCount[sid]++
				u.LastPlayed[sid] = now
				appDB.Users[uname] = u
			}
			mu.Unlock()
			saveDB()
		}(id, currentUser)

		if strings.HasPrefix(id, "js-") {
			streamURL := getJioStreamURL(id)
			if streamURL == "" {
				writeSubsonicError(w, 70, "JioSaavn stream not available", f)
				return
			}
			http.Redirect(w, r, streamURL, 302)
			return
		}

		newURL := *r.URL
		q := newURL.Query()
		q.Set("url", "https://www.youtube.com/watch?v="+id)
		newURL.RawQuery = q.Encode()
		r.URL = &newURL
		playHandler(w, r)

	case "getcoverart":
		id := r.URL.Query().Get("id")
		cleanID := strings.TrimPrefix(strings.TrimPrefix(id, "al-"), "ar-")

		mu.RLock()
		s, ok := appDB.Songs[cleanID]
		mu.RUnlock()

		if ok && s.CoverArt != "" {
			http.Redirect(w, r, s.CoverArt, 302)
			return
		}

		if strings.HasPrefix(cleanID, "js-") {
			imgURL := getJioCoverArt(cleanID)
			if imgURL != "" {
				http.Redirect(w, r, imgURL, 302)
				return
			}
		}

		if len(cleanID) == 11 && !strings.HasPrefix(cleanID, "js-") {
			http.Redirect(w, r, "https://i.ytimg.com/vi/"+cleanID+"/hqdefault.jpg", 302)
			return
		}

		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d, 0xb0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82})

	case "search2", "search3":
		q := strings.TrimSpace(r.URL.Query().Get("query"))
		qLower := strings.ToLower(q)

		songCount := 25
		if sc := r.URL.Query().Get("songCount"); sc != "" {
			if v, err := strconv.Atoi(sc); err == nil && v > 0 {
				songCount = v
			}
		}
		if songCount > 50 {
			songCount = 50
		}

		songOffset := 0
		if so := r.URL.Query().Get("songOffset"); so != "" {
			if v, err := strconv.Atoi(so); err == nil && v >= 0 {
				songOffset = v
			}
		}

		var allMatched []Song
		if val, cached := searchCache.Load(qLower); cached {
			allMatched = val.([]Song)
		} else {
			mu.RLock()
			for _, s := range appDB.Songs {
				if qLower == "" || strings.Contains(strings.ToLower(s.Title), qLower) || strings.Contains(strings.ToLower(s.Artist), qLower) {
					allMatched = append(allMatched, s)
				}
			}
			mu.RUnlock()

			if len(allMatched) < 30 && len(q) >= 2 {
				seen := map[string]bool{}
				for _, s := range allMatched {
					seen[s.YTID] = true
				}

				var wg sync.WaitGroup
				var jioResults []Song
				var ytResults []Song

				wg.Add(2)
				go func() {
					defer wg.Done()
					jioResults = searchJioSaavn(q, 25)
				}()
				go func() {
					defer wg.Done()
					ytResults = searchYouTube(q, 6)
				}()
				wg.Wait()

				for _, js := range jioResults {
					if !seen[js.YTID] {
						allMatched = append(allMatched, js)
						seen[js.YTID] = true
						ensureSongInMemory(js)
					}
				}
				for _, ys := range ytResults {
					if !seen[ys.YTID] {
						allMatched = append(allMatched, ys)
						seen[ys.YTID] = true
						ensureSongInMemory(ys)
					}
				}
			}
			searchCache.Store(qLower, allMatched)
		}

		totalSongs := len(allMatched)
		var pagedSongs []Song
		if songOffset < totalSongs {
			end := songOffset + songCount
			if end > totalSongs {
				end = totalSongs
			}
			pagedSongs = allMatched[songOffset:end]
		}

		songs := []map[string]interface{}{}
		for _, s := range pagedSongs {
			songs = append(songs, songToMap(s, currentUserPtr))
		}

		if f == "json" {
			searchObj := map[string]interface{}{
				"artist": []interface{}{},
				"album":  []interface{}{},
				"song":   songs,
			}
			writeSubsonicOK(w, f, map[string]interface{}{
				"searchResult2": searchObj,
				"searchResult3": searchObj,
			})
		} else {
			rootTag := "searchResult3"
			if path == "search2" {
				rootTag = "searchResult2"
			}
			var b strings.Builder
			b.WriteString(fmt.Sprintf(`<%s>`, rootTag))
			for _, s := range pagedSongs {
				b.WriteString(fmt.Sprintf(`<song id="%s" title="%s" artist="%s" album="Online Search" duration="%d" coverArt="%s"/>`,
					s.YTID, xmlEscape(s.Title), xmlEscape(s.Artist), s.Duration, s.YTID))
			}
			b.WriteString(fmt.Sprintf(`</%s>`, rootTag))
			writeSubsonicOK(w, f, b.String())
		}

	case "getalbumlist", "getalbumlist2":
		listType := r.URL.Query().Get("type")
		if listType == "" {
			listType = "newest"
		}
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		if size <= 0 {
			size = 20
		}
		mu.RLock()
		type item struct {
			s Song
			t time.Time
		}
		var list []item
		for _, s := range appDB.Songs {
			var t time.Time
			switch listType {
			case "recent", "recentlyplayed":
				t, _ = time.Parse(time.RFC3339, s.LastPlayed)
			case "starred":
				if currentUserPtr != nil && currentUserPtr.Starred != nil && !currentUserPtr.Starred[s.YTID] {
					continue
				}
				t, _ = time.Parse(time.RFC3339, s.LastPlayed)
				if t.IsZero() {
					t, _ = time.Parse(time.RFC3339, s.AddedAt)
				}
			default:
				t, _ = time.Parse(time.RFC3339, s.AddedAt)
			}
			list = append(list, item{s, t})
		}
		mu.RUnlock()

		switch listType {
		case "frequent":
			sort.Slice(list, func(i, j int) bool { return list[i].s.PlayCount > list[j].s.PlayCount })
		case "highest":
			sort.Slice(list, func(i, j int) bool { return list[i].s.Rating > list[j].s.Rating })
		case "random":
			for i := range list {
				j := int(time.Now().UnixNano()+int64(i)) % len(list)
				list[i], list[j] = list[j], list[i]
			}
		default:
			sort.Slice(list, func(i, j int) bool { return list[i].t.After(list[j].t) })
		}

		if len(list) > size {
			list = list[:size]
		}
		albums := []map[string]interface{}{}
		for _, it := range list {
			albums = append(albums, map[string]interface{}{
				"id":        "al-" + it.s.YTID,
				"name":      it.s.Title,
				"artist":    it.s.Artist,
				"artistId":  "ar-1",
				"coverArt":  it.s.YTID,
				"songCount": 1,
				"created":   it.s.AddedAt,
				"playCount": it.s.PlayCount,
			})
		}

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"albumList": map[string]interface{}{"album": albums}, "albumList2": map[string]interface{}{"album": albums}})
		} else {
			var b strings.Builder
			b.WriteString(`<albumList2>`)
			for _, a := range albums {
				b.WriteString(fmt.Sprintf(`<album id="%v" name="%v" artist="%v" coverArt="%v" songCount="1"/>`, a["id"], xmlEscape(fmt.Sprint(a["name"])), xmlEscape(fmt.Sprint(a["artist"])), a["coverArt"]))
			}
			b.WriteString(`</albumList2>`)
			writeSubsonicOK(w, f, b.String())
		}

	case "getrandomsongs":
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		if size <= 0 {
			size = 20
		}
		mu.RLock()
		var songs []Song
		for _, s := range appDB.Songs {
			songs = append(songs, s)
		}
		mu.RUnlock()

		for i := range songs {
			j := int(time.Now().UnixNano()+int64(i)) % len(songs)
			songs[i], songs[j] = songs[j], songs[i]
		}
		if len(songs) > size {
			songs = songs[:size]
		}
		out := []map[string]interface{}{}
		for _, s := range songs {
			out = append(out, songToMap(s, currentUserPtr))
		}
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"randomSongs": map[string]interface{}{"song": out}})
		} else {
			var b strings.Builder
			b.WriteString(`<randomSongs>`)
			for _, s := range songs {
				b.WriteString(fmt.Sprintf(`<song id="%s" title="%s" artist="%s"/>`, s.YTID, xmlEscape(s.Title), xmlEscape(s.Artist)))
			}
			b.WriteString(`</randomSongs>`)
			writeSubsonicOK(w, f, b.String())
		}

	case "star":
		id := r.URL.Query().Get("id")
		if id != "" && currentUser != "" {
			mu.Lock()
			user, ok := appDB.Users[currentUser]
			if !ok {
				user = User{
					Username:  currentUser,
					Starred:   make(map[string]bool),
					PlayCount: make(map[string]int),
				}
			}
			if user.Starred == nil {
				user.Starred = make(map[string]bool)
			}
			user.Starred[id] = true
			appDB.Users[currentUser] = user
			mu.Unlock()
			saveDB()
		}
		writeSubsonicOK(w, f, map[string]interface{}{})

	case "unstar":
		id := r.URL.Query().Get("id")
		if id != "" && currentUser != "" {
			mu.Lock()
			if user, ok := appDB.Users[currentUser]; ok && user.Starred != nil {
				delete(user.Starred, id)
				appDB.Users[currentUser] = user
			}
			mu.Unlock()
			saveDB()
		}
		writeSubsonicOK(w, f, map[string]interface{}{})

	case "getstarred", "getstarred2":
		var starred []map[string]interface{}
		mu.RLock()
		if currentUserPtr != nil && len(currentUserPtr.Starred) > 0 {
			for sid := range currentUserPtr.Starred {
				if s, ok := appDB.Songs[sid]; ok {
					starred = append(starred, songToMap(s, currentUserPtr))
				}
			}
		}
		mu.RUnlock()

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"starred": map[string]interface{}{"song": starred}, "starred2": map[string]interface{}{"song": starred}})
		} else {
			var b strings.Builder
			b.WriteString(`<starred2>`)
			for _, s := range starred {
				b.WriteString(fmt.Sprintf(`<song id="%v" title="%v"/>`, s["id"], xmlEscape(fmt.Sprint(s["title"]))))
			}
			b.WriteString(`</starred2>`)
			writeSubsonicOK(w, f, b.String())
		}

	case "setrating":
		id := r.URL.Query().Get("id")
		rating, _ := strconv.Atoi(r.URL.Query().Get("rating"))
		mu.Lock()
		if s, ok := appDB.Songs[id]; ok {
			s.Rating = rating
			appDB.Songs[id] = s
			mu.Unlock()
			saveDB()
		} else {
			mu.Unlock()
		}
		writeSubsonicOK(w, f, map[string]interface{}{})

	case "scrobble":
		id := r.URL.Query().Get("id")
		if id != "" {
			mu.Lock()
			if s, ok := appDB.Songs[id]; ok {
				s.PlayCount++
				s.LastPlayed = time.Now().Format(time.RFC3339)
				appDB.Songs[id] = s
			}
			if u, ok := appDB.Users[currentUser]; ok {
				if u.PlayCount == nil {
					u.PlayCount = make(map[string]int)
				}
				if u.LastPlayed == nil {
					u.LastPlayed = make(map[string]string)
				}
				u.PlayCount[id]++
				u.LastPlayed[id] = time.Now().Format(time.RFC3339)
				appDB.Users[currentUser] = u
			}
			mu.Unlock()
			saveDB()
		}
		writeSubsonicOK(w, f, map[string]interface{}{})

	case "getplaylists":
		mu.RLock()
		var pls []map[string]interface{}
		for _, p := range appDB.Playlists {
			if p.Owner == currentUser || p.Public || isAdmin(r) || p.Owner == "admin" {
				pls = append(pls, map[string]interface{}{
					"id": p.ID, "name": p.Name, "songCount": len(p.SongIDs),
					"created": p.Created, "changed": p.Changed, "owner": p.Owner, "public": p.Public,
					"comment": p.YTURL,
				})
			}
		}
		mu.RUnlock()

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"playlists": map[string]interface{}{"playlist": pls}})
		} else {
			var b strings.Builder
			b.WriteString(`<playlists>`)
			for _, p := range pls {
				b.WriteString(fmt.Sprintf(`<playlist id="%v" name="%v" songCount="%v"/>`, p["id"], xmlEscape(fmt.Sprint(p["name"])), p["songCount"]))
			}
			b.WriteString(`</playlists>`)
			writeSubsonicOK(w, f, b.String())
		}

	case "getplaylist":
		id := r.URL.Query().Get("id")
		mu.RLock()
		pl, ok := appDB.Playlists[id]
		var songs []map[string]interface{}
		if ok {
			for _, sid := range pl.SongIDs {
				if s, ok := appDB.Songs[sid]; ok {
					songs = append(songs, songToMap(s, currentUserPtr))
				}
			}
		}
		mu.RUnlock()

		if !ok {
			writeSubsonicError(w, 70, "Playlist not found", f)
			return
		}

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"playlist": map[string]interface{}{
					"id": pl.ID, "name": pl.Name, "songCount": len(pl.SongIDs), "entry": songs,
				},
			})
		} else {
			var b strings.Builder
			b.WriteString(fmt.Sprintf(`<playlist id="%s" name="%s" songCount="%d">`, pl.ID, xmlEscape(pl.Name), len(pl.SongIDs)))
			for _, s := range songs {
				b.WriteString(fmt.Sprintf(`<entry id="%v" title="%v"/>`, s["id"], xmlEscape(fmt.Sprint(s["title"]))))
			}
			b.WriteString(`</playlist>`)
			writeSubsonicOK(w, f, b.String())
		}

	case "createplaylist":
		name := r.URL.Query().Get("name")
		if name == "" {
			name = "New Playlist"
		}
		id := fmt.Sprintf("pl-%d", time.Now().UnixNano())
		now := time.Now().Format(time.RFC3339)
		ids := r.URL.Query()["songId"]

		pl := Playlist{
			ID: id, Name: name, SongIDs: ids, Created: now,
			Changed: now, Owner: currentUser, Public: false,
		}
		mu.Lock()
		appDB.Playlists[id] = pl
		mu.Unlock()
		saveDB()

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"playlist": map[string]interface{}{"id": id, "name": name, "songCount": len(ids)}})
		} else {
			writeSubsonicOK(w, f, fmt.Sprintf(`<playlist id="%s" name="%s" songCount="%d"/>`, id, xmlEscape(name), len(ids)))
		}

	case "updateplaylist":
		id := r.URL.Query().Get("id")
		mu.Lock()
		pl, ok := appDB.Playlists[id]
		if !ok {
			mu.Unlock()
			writeSubsonicError(w, 70, "Playlist not found", f)
			return
		}
		if pl.Owner != currentUser && !isAdmin(r) {
			mu.Unlock()
			writeSubsonicError(w, 50, "Unauthorized: Not your playlist", f)
			return
		}
		if name := r.URL.Query().Get("name"); name != "" {
			pl.Name = name
		}
		for _, sid := range r.URL.Query()["songIdToAdd"] {
			found := false
			for _, existing := range pl.SongIDs {
				if existing == sid {
					found = true
					break
				}
			}
			if !found {
				pl.SongIDs = append(pl.SongIDs, sid)
			}
		}
		if idxStr := r.URL.Query().Get("songIndexToRemove"); idxStr != "" {
			idx, _ := strconv.Atoi(idxStr)
			if idx >= 0 && idx < len(pl.SongIDs) {
				pl.SongIDs = append(pl.SongIDs[:idx], pl.SongIDs[idx+1:]...)
			}
		}
		pl.Changed = time.Now().Format(time.RFC3339)
		appDB.Playlists[id] = pl
		mu.Unlock()
		saveDB()
		writeSubsonicOK(w, f, map[string]interface{}{})

	case "deleteplaylist":
		id := r.URL.Query().Get("id")
		mu.Lock()
		pl, ok := appDB.Playlists[id]
		if ok && (pl.Owner == currentUser || isAdmin(r)) {
			delete(appDB.Playlists, id)
			mu.Unlock()
			saveDB()
		} else {
			mu.Unlock()
		}
		writeSubsonicOK(w, f, map[string]interface{}{})

	case "getlyrics":
		id := r.URL.Query().Get("id")
		artist := r.URL.Query().Get("artist")
		title := r.URL.Query().Get("title")

		cleanID := strings.TrimPrefix(strings.TrimPrefix(id, "al-"), "ar-")

		var lyrics string
		if cleanID != "" {
			lyrics = getLyricsForSong(cleanID)
		}
		if lyrics == "" && title != "" {
			lyrics = fetchLyrics(artist, title)
		}

		if strings.TrimSpace(lyrics) == "" {
			writeSubsonicError(w, 70, "Lyrics not found", f)
			return
		}

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"lyrics": map[string]interface{}{
					"artist": artist,
					"title":  title,
					"value":  lyrics,
				},
			})
		} else {
			writeSubsonicOK(w, f, fmt.Sprintf(`<lyrics artist="%s" title="%s">%s</lyrics>`, xmlEscape(artist), xmlEscape(title), xmlEscape(lyrics)))
		}

	case "getlyricsbysongid":
		id := r.URL.Query().Get("id")
		cleanID := strings.TrimPrefix(strings.TrimPrefix(id, "al-"), "ar-")
		if cleanID == "" {
			writeSubsonicError(w, 10, "Missing id", f)
			return
		}

		lyrics := getLyricsForSong(cleanID)
		if strings.TrimSpace(lyrics) == "" {
			writeSubsonicError(w, 70, "Lyrics not found", f)
			return
		}

		mu.RLock()
		s := appDB.Songs[cleanID]
		mu.RUnlock()

		var lineObjs []map[string]interface{}
		for _, line := range strings.Split(lyrics, "\n") {
			l := strings.TrimSpace(line)
			if l != "" {
				lineObjs = append(lineObjs, map[string]interface{}{"value": l})
			}
		}

		if len(lineObjs) == 0 {
			writeSubsonicError(w, 70, "Lyrics not found", f)
			return
		}

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"lyricsList": map[string]interface{}{
					"structuredLyrics": []map[string]interface{}{{
						"lang":          "und",
						"displayArtist": s.Artist,
						"displayTitle":  s.Title,
						"offset":        0,
						"synced":        false,
						"line":          lineObjs,
					}},
				},
			})
		} else {
			var b strings.Builder
			b.WriteString(`<lyricsList><structuredLyrics lang="und" synced="false">`)
			for _, lo := range lineObjs {
				b.WriteString(fmt.Sprintf(`<line value="%s"/>`, xmlEscape(fmt.Sprint(lo["value"]))))
			}
			b.WriteString(`</structuredLyrics></lyricsList>`)
			writeSubsonicOK(w, f, b.String())
		}

	case "getuser":
		username := r.URL.Query().Get("username")
		if username == "" {
			username = currentUser
		}
		mu.RLock()
		u, exists := appDB.Users[username]
		mu.RUnlock()
		if !exists && username != "admin" {
			writeSubsonicError(w, 70, "User not found", f)
			return
		}
		_ = u
		adminU, _ := getAdminCreds()
		userMap := map[string]interface{}{
			"username":          username,
			"email":             username + "@local",
			"adminRole":         username == adminU,
			"settingsRole":      true,
			"downloadRole":      true,
			"uploadRole":        true,
			"playlistRole":      true,
			"coverArtRole":      true,
			"commentRole":       true,
			"podcastRole":       true,
			"streamRole":        true,
			"jukeboxRole":       false,
			"shareRole":         false,
			"scrobblingEnabled": true,
		}
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"user": userMap})
		} else {
			writeSubsonicOK(w, f, fmt.Sprintf(`<user username="%s" email="%s@local" adminRole="%v" settingsRole="true" downloadRole="true" uploadRole="true" playlistRole="true" coverArtRole="true" commentRole="true" podcastRole="true" streamRole="true" jukeboxRole="false" shareRole="false" scrobblingEnabled="true"/>`, username, username, username == adminU))
		}

	case "getusers":
		if !isAdmin(r) {
			writeSubsonicError(w, 50, "Admin required", f)
			return
		}
		mu.RLock()
		var uList []map[string]interface{}
		adminU, _ := getAdminCreds()
		for _, u := range appDB.Users {
			uList = append(uList, map[string]interface{}{
				"username":     u.Username,
				"email":        u.Username + "@local",
				"adminRole":    u.Username == adminU,
				"playlistRole": true,
				"streamRole":   true,
			})
		}
		mu.RUnlock()

		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"users": map[string]interface{}{"user": uList},
			})
		} else {
			var b strings.Builder
			b.WriteString(`<users>`)
			for _, u := range uList {
				b.WriteString(fmt.Sprintf(`<user username="%s" adminRole="%v"/>`, u["username"], u["adminRole"]))
			}
			b.WriteString(`</users>`)
			writeSubsonicOK(w, f, b.String())
		}

	case "createuser":
		if !isAdmin(r) {
			writeSubsonicError(w, 50, "User is not authorized", f)
			return
		}
		username := strings.TrimSpace(r.URL.Query().Get("username"))
		password := r.URL.Query().Get("password")
		if username == "" || password == "" {
			writeSubsonicError(w, 10, "Required parameter is missing", f)
			return
		}
		mu.Lock()
		if _, exists := appDB.Users[username]; exists {
			mu.Unlock()
			writeSubsonicError(w, 10, "User already exists", f)
			return
		}
		appDB.Users[username] = User{
			Username:   username,
			Password:   password,
			Starred:    make(map[string]bool),
			PlayCount:  make(map[string]int),
			LastPlayed: make(map[string]string),
		}
		mu.Unlock()
		saveDB()
		writeSubsonicOK(w, f, map[string]interface{}{})

	case "changepassword":
		username := strings.TrimSpace(r.URL.Query().Get("username"))
		newPass := r.URL.Query().Get("password")
		if username == "" {
			username = currentUser
		}
		if username != currentUser && !isAdmin(r) {
			writeSubsonicError(w, 50, "Unauthorized to change another user's password", f)
			return
		}
		mu.Lock()
		if u, ok := appDB.Users[username]; ok {
			u.Password = newPass
			appDB.Users[username] = u
			mu.Unlock()
			saveDB()
			writeSubsonicOK(w, f, map[string]interface{}{})
			return
		}
		mu.Unlock()
		writeSubsonicError(w, 70, "User not found", f)

	case "deleteuser":
		if !isAdmin(r) {
			writeSubsonicError(w, 50, "Admin required", f)
			return
		}
		username := strings.TrimSpace(r.URL.Query().Get("username"))
		adminU, _ := getAdminCreds()
		if username == "" || username == adminU {
			http.Error(w, "Cannot delete admin account", 400)
			return
		}
		mu.Lock()
		delete(appDB.Users, username)
		mu.Unlock()
		saveDB()
		writeSubsonicOK(w, f, map[string]interface{}{})

	default:
		writeSubsonicOK(w, f, map[string]interface{}{})
	}
}

// -------------------- Streaming Engine --------------------

func playHandler(w http.ResponseWriter, r *http.Request) {
	ytUrl := r.URL.Query().Get("url")
	if ytUrl == "" {
		http.Error(w, "Query parameter url is required", 400)
		return
	}

	ytID := ytUrl
	if strings.Contains(ytUrl, "youtu.be/") {
		ytID = strings.Split(strings.Split(ytUrl, "youtu.be/")[1], "?")[0]
		ytID = strings.Split(ytID, "&")[0]
	} else if strings.Contains(ytUrl, "v=") {
		ytID = strings.Split(strings.Split(ytUrl, "v=")[1], "&")[0]
	}

	mu.RLock()
	song, exists := appDB.Songs[ytID]
	mu.RUnlock()

	if exists && song.FileID != "" {
		token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
		fp := song.FilePath
		if fp == "" {
			fp = getTelegramFilePath(song.FileID)
		}
		if token != "" && fp != "" {
			proxyTelegramAudio(w, r, token, fp)
			return
		}
	}

	sem <- struct{}{}
	defer func() { <-sem }()

	tmpFile := filepath.Join("/tmp", ytID+".m4a")
	if _, err := os.Stat(tmpFile); err == nil {
		w.Header().Set("Content-Type", "audio/mp4")
		http.ServeFile(w, r, tmpFile)
		return
	}

	cookieArgs := getCookiesArg()
	title, artist := getYTTitle(ytUrl, cookieArgs)
	if title == "" {
		title = ytID
	}
	if artist == "" {
		artist = "YouTube"
	}

	// 90-second context timeout prevents permanent concurrency freeze
	execCtx, execCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer execCancel()

	var ytArgs []string
	if len(cookieArgs) > 0 {
		ytArgs = append(ytArgs, cookieArgs...)
	}
	ytArgs = append(ytArgs,
		"-x", "--audio-format", "m4a",
		"--audio-quality", "0",
		"-f", "bestaudio/ba/b",
		"--no-playlist",
		"--no-check-certificate",
		"--no-warnings",
		"--geo-bypass",
		"--socket-timeout", "15",
		"--extractor-args", "youtube:player_client=android,web",
		"-o", filepath.Join("/tmp", "%(id)s.%(ext)s"),
		ytUrl,
	)

	cmd := exec.CommandContext(execCtx, "yt-dlp", ytArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmpFile)
		log.Printf("[YT Stream Error] yt-dlp failed for %s: %v | Log: %s", ytUrl, err, string(out))
		http.Error(w, fmt.Sprintf("yt-dlp error: %v\n%s", err, string(out)), 500)
		return
	}

	now := time.Now().Format(time.RFC3339)
	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	chatID := strings.TrimSpace(os.Getenv("CHANNEL_ID"))
	fileID := ""
	fp := ""

	if token != "" && chatID != "" {
		if f, err := os.Open(tmpFile); err == nil {
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)
			part, _ := writer.CreateFormFile("audio", filepath.Base(tmpFile))
			io.Copy(part, f)
			f.Close()
			writer.WriteField("chat_id", chatID)
			writer.WriteField("caption", title)
			writer.Close()

			req, _ := http.NewRequest("POST", fmt.Sprintf("https://api.telegram.org/bot%s/sendAudio", token), body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			client := &http.Client{Timeout: 60 * time.Second}
			if resp, err := client.Do(req); err == nil {
				defer resp.Body.Close()
				var res struct {
					Ok     bool `json:"ok"`
					Result struct {
						Audio struct {
							FileID string `json:"file_id"`
						} `json:"audio"`
					} `json:"result"`
				}
				if json.NewDecoder(resp.Body).Decode(&res) == nil && res.Ok {
					fileID = res.Result.Audio.FileID
					fp = getTelegramFilePath(fileID)
				}
			}
		}
	}

	mu.Lock()
	existing := appDB.Songs[ytID]
	s := Song{
		YTID: ytID, Title: title + " [YT]", Artist: artist,
		FileID: fileID, FilePath: fp,
		AddedAt: existing.AddedAt, PlayCount: existing.PlayCount + 1,
		LastPlayed: now, Starred: existing.Starred, Rating: existing.Rating,
		Duration: existing.Duration,
		CoverArt: fmt.Sprintf("https://i.ytimg.com/vi/%s/hqdefault.jpg", ytID),
	}
	if s.AddedAt == "" {
		s.AddedAt = now
	}
	appDB.Songs[ytID] = s
	mu.Unlock()
	saveDB()

	w.Header().Set("Content-Type", "audio/mp4")
	http.ServeFile(w, r, tmpFile)
}

// -------------------- Multi-User True Mirror Sync --------------------

func fetchYTPlaylistSongs(playlistURL string, maxVideos int) ([]Song, error) {
	if maxVideos <= 0 {
		maxVideos = 50
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	cookieArgs := getCookiesArg()
	var args []string
	if len(cookieArgs) > 0 {
		args = append(args, cookieArgs...)
	}
	args = append(args,
		"--flat-playlist",
		"--print", "%(id)s|||%(title)s|||%(uploader)s",
		"--no-download",
		"--playlist-end", fmt.Sprintf("%d", maxVideos),
		"--extractor-args", "youtube:player_client=android,web",
		playlistURL,
	)

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp error: %v", err)
	}

	now := time.Now().Format(time.RFC3339)
	var songs []Song
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "WARNING") || strings.HasPrefix(line, "ERROR") {
			continue
		}
		parts := strings.SplitN(line, "|||", 3)
		if len(parts) < 2 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		title := strings.TrimSpace(parts[1])
		artist := "YouTube"
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
			artist = strings.TrimSpace(parts[2])
		}
		if id == "" || title == "" {
			continue
		}
		songs = append(songs, Song{
			YTID:     id,
			Title:    title + " [YT]",
			Artist:   artist,
			AddedAt:  now,
			Duration: 180,
			CoverArt: fmt.Sprintf("https://i.ytimg.com/vi/%s/hqdefault.jpg", id),
		})
	}
	return songs, nil
}

func importOrSyncYouTubePlaylist(playlistURL string, maxVideos int, owner string, plName string) (int, string, error) {
	freshSongs, err := fetchYTPlaylistSongs(playlistURL, maxVideos)
	if err != nil {
		return 0, "", err
	}

	mu.Lock()
	defer mu.Unlock()

	now := time.Now().Format(time.RFC3339)
	var currentIDs []string

	addedCount := 0
	for _, s := range freshSongs {
		currentIDs = append(currentIDs, s.YTID)
		if _, exists := appDB.Songs[s.YTID]; !exists {
			appDB.Songs[s.YTID] = s
			addedCount++
		}
	}

	var targetPlID string
	for id, pl := range appDB.Playlists {
		if pl.Owner == owner && pl.YTURL == playlistURL {
			targetPlID = id
			break
		}
	}

	if targetPlID != "" {
		pl := appDB.Playlists[targetPlID]
		if plName != "" {
			pl.Name = plName
		}
		pl.SongIDs = currentIDs
		pl.Changed = now
		appDB.Playlists[targetPlID] = pl
		return addedCount, targetPlID, nil
	}

	if plName == "" {
		plName = "YT Playlist " + time.Now().Format("02 Jan 15:04")
	}
	targetPlID = fmt.Sprintf("pl-%d", time.Now().UnixNano())
	appDB.Playlists[targetPlID] = Playlist{
		ID:      targetPlID,
		Name:    plName,
		SongIDs: currentIDs,
		Created: now,
		Changed: now,
		Owner:   owner,
		Public:  false,
		YTURL:   playlistURL,
	}

	return addedCount, targetPlID, nil
}

func importPlaylistHandler(w http.ResponseWriter, r *http.Request) {
	currentUser := getCurrentUser(r)
	if currentUser == "" {
		adminU, adminP := getAdminCreds()
		if r.URL.Query().Get("u") == adminU && r.URL.Query().Get("p") == adminP {
			currentUser = adminU
		} else {
			http.Error(w, "Unauthorized: Login required", 401)
			return
		}
	}

	playlistURL := r.URL.Query().Get("url")
	if playlistURL == "" {
		http.Error(w, "missing url param", 400)
		return
	}

	plName := strings.TrimSpace(r.URL.Query().Get("name"))
	maxV := 40
	if m := r.URL.Query().Get("max"); m != "" {
		if v, err := strconv.Atoi(m); err == nil && v > 0 {
			maxV = v
		}
	}

	added, plID, err := importOrSyncYouTubePlaylist(playlistURL, maxV, currentUser, plName)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	saveDB()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","newSongsAdded":%d,"playlistId":"%s","owner":"%s","message":"Mirror synced with YouTube!"}`, added, plID, currentUser)
}

// -------------------- Background Mirror Sync Engine --------------------

func startBackgroundSync() {
	go func() {
		time.Sleep(30 * time.Second)
		for {
			syncAllLinkedPlaylists()
			time.Sleep(6 * time.Hour)
		}
	}()
}

func syncAllLinkedPlaylists() {
	mu.RLock()
	type plTask struct {
		id    string
		url   string
		owner string
		name  string
	}
	var tasks []plTask
	for id, pl := range appDB.Playlists {
		if pl.YTURL != "" {
			tasks = append(tasks, plTask{id: id, url: pl.YTURL, owner: pl.Owner, name: pl.Name})
		}
	}
	mu.RUnlock()

	if len(tasks) == 0 {
		return
	}

	log.Printf("[AutoSync] Mirror syncing %d YouTube playlists across all users...", len(tasks))
	hasChanges := false

	for _, t := range tasks {
		added, _, err := importOrSyncYouTubePlaylist(t.url, 50, t.owner, t.name)
		if err != nil {
			log.Printf("[AutoSync] Failed mirroring %s: %v", t.name, err)
			continue
		}
		if added > 0 {
			hasChanges = true
		}
		time.Sleep(3 * time.Second)
	}

	if hasChanges {
		log.Println("[AutoSync] Changes detected during mirror sync. Uploading backup to Telegram...")
		saveDB()
	}
}

// -------------------- User & Playlist REST Endpoints --------------------

func listHandler(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(appDB.Songs)
}

func dbHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, dbPath)
}

func myPlaylistsHandler(w http.ResponseWriter, r *http.Request) {
	u := getCurrentUser(r)
	if u == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}

	mu.RLock()
	var list []Playlist
	for _, p := range appDB.Playlists {
		if p.Owner == u || isAdmin(r) {
			list = append(list, p)
		}
	}
	mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func syncSpecificPlaylistHandler(w http.ResponseWriter, r *http.Request) {
	u := getCurrentUser(r)
	plID := r.URL.Query().Get("id")

	mu.RLock()
	pl, exists := appDB.Playlists[plID]
	mu.RUnlock()

	if !exists {
		http.Error(w, "Playlist not found", 404)
		return
	}
	if pl.Owner != u && !isAdmin(r) {
		http.Error(w, "Unauthorized", 403)
		return
	}
	if pl.YTURL == "" {
		http.Error(w, "Not a YouTube synced playlist", 400)
		return
	}

	added, _, err := importOrSyncYouTubePlaylist(pl.YTURL, 50, pl.Owner, pl.Name)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	saveDB()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","added":%d,"message":"Synced successfully"}`, added)
}

func myFavoritesHandler(w http.ResponseWriter, r *http.Request) {
	u := getCurrentUser(r)
	if u == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}

	mu.RLock()
	user, exists := appDB.Users[u]
	var songs []Song
	if exists && user.Starred != nil {
		for sid := range user.Starred {
			if s, ok := appDB.Songs[sid]; ok {
				songs = append(songs, s)
			}
		}
	}
	mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(songs)
}

func registerUserHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if r.Method == "POST" {
		json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Username == "" {
		req.Username = r.URL.Query().Get("username")
		req.Password = r.URL.Query().Get("password")
	}

	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		http.Error(w, `{"status":"error","message":"username and password required"}`, 400)
		return
	}

	mu.Lock()
	if _, exists := appDB.Users[req.Username]; exists {
		mu.Unlock()
		http.Error(w, `{"status":"error","message":"user already exists"}`, 400)
		return
	}

	appDB.Users[req.Username] = User{
		Username:   req.Username,
		Password:   req.Password,
		Starred:    make(map[string]bool),
		PlayCount:  make(map[string]int),
		LastPlayed: make(map[string]string),
	}
	mu.Unlock()
	saveDB()

	log.Printf("[Auth] User registered: %s", req.Username)
	fmt.Fprintf(w, `{"status":"ok","message":"Registration successful","username":"%s"}`, req.Username)
}

func listUsersHandler(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Error(w, "Admin access required", 403)
		return
	}
	mu.RLock()
	defer mu.RUnlock()
	type uinfo struct {
		Username  string `json:"username"`
		Stars     int    `json:"stars"`
		Playlists int    `json:"playlists"`
	}
	var list []uinfo
	for _, u := range appDB.Users {
		pls := 0
		for _, p := range appDB.Playlists {
			if p.Owner == u.Username {
				pls++
			}
		}
		list = append(list, uinfo{Username: u.Username, Stars: len(u.Starred), Playlists: pls})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"users": list})
}

func deleteUserHandler(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Error(w, "Admin access required", 403)
		return
	}
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	adminU, _ := getAdminCreds()
	if username == "" || username == adminU {
		http.Error(w, "Cannot delete admin account", 400)
		return
	}
	mu.Lock()
	delete(appDB.Users, username)
	mu.Unlock()
	saveDB()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","deleted":"%s"}`, username)
}

// -------------------- Admin Login API --------------------

func adminLoginHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	user := strings.TrimSpace(body.User)
	pass := body.Pass
	adminUser, adminPass := getAdminCreds()

	ok := false
	isAdminRole := false
	if user == adminUser && pass == adminPass {
		ok = true
		isAdminRole = true
	} else {
		mu.RLock()
		if u, exists := appDB.Users[user]; exists && u.Password == pass {
			ok = true
		}
		mu.RUnlock()
	}

	w.Header().Set("Content-Type", "application/json")
	if ok {
		token := fmt.Sprintf("tok_%s_%d", user, time.Now().Unix())
		fmt.Fprintf(w, `{"ok":true,"token":"%s","user":"%s","isAdmin":%v}`, token, user, isAdminRole)
	} else {
		fmt.Fprintf(w, `{"ok":false,"error":"Invalid username or password"}`)
	}
}

// -------------------- Lyrics Provider (JioSaavn + LRCLib) --------------------

func fetchJioLyrics(jioID string) string {
	cleanID := strings.TrimPrefix(jioID, "js-")
	apiURL := fmt.Sprintf("%s/lyrics?id=%s", jioAPI, cleanID)
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var res struct {
		Status string `json:"status"`
		Data   struct {
			Lyrics string `json:"lyrics"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&res) == nil && (res.Status == "SUCCESS" || res.Status == "success") {
		raw := res.Data.Lyrics
		raw = strings.ReplaceAll(raw, "<br>", "\n")
		raw = strings.ReplaceAll(raw, "<br/>", "\n")
		raw = strings.ReplaceAll(raw, "<br />", "\n")
		return html.UnescapeString(strings.TrimSpace(raw))
	}
	return ""
}

func fetchLyrics(artist, title string) string {
	clean := title
	for _, cut := range []string{"(Official Video)", "(Official Audio)", "(Lyrics)", "(Audio)", "[Official Video]", "|", " - Topic", "[Jio]", "[YT]"} {
		clean = strings.ReplaceAll(clean, cut, "")
	}
	clean = strings.TrimSpace(clean)

	client := &http.Client{Timeout: 5 * time.Second}
	q := url.QueryEscape(clean)
	if artist != "" && artist != "YouTube" && artist != "JioSaavn" {
		q = url.QueryEscape(artist + " " + clean)
	}
	resp, err := client.Get("https://lrclib.net/api/search?q=" + q)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			var results []struct {
				PlainLyrics string `json:"plainLyrics"`
			}
			if json.NewDecoder(resp.Body).Decode(&results) == nil && len(results) > 0 && results[0].PlainLyrics != "" {
				return strings.TrimSpace(results[0].PlainLyrics)
			}
		}
	}
	return ""
}

func getLyricsForSong(id string) string {
	mu.RLock()
	s, ok := appDB.Songs[id]
	mu.RUnlock()
	if !ok {
		return ""
	}
	if s.Lyrics != "" {
		return s.Lyrics
	}

	var lyrics string
	if strings.HasPrefix(id, "js-") {
		lyrics = fetchJioLyrics(id)
	}
	if lyrics == "" {
		lyrics = fetchLyrics(s.Artist, s.Title)
	}

	if lyrics != "" {
		mu.Lock()
		if song, ok := appDB.Songs[id]; ok {
			song.Lyrics = lyrics
			appDB.Songs[id] = song
		}
		mu.Unlock()
		saveDB()
	}
	return lyrics
}

// -------------------- JioSaavn Engine --------------------

const jioAPI = "https://jiosaavn-api-three-ashy.vercel.app"

func decryptJioURL(encrypted string) string {
	if encrypted == "" {
		return ""
	}
	switch len(encrypted) % 4 {
	case 2:
		encrypted += "=="
	case 3:
		encrypted += "="
	}
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		data, err = base64.URLEncoding.DecodeString(encrypted)
		if err != nil {
			return ""
		}
	}
	block, err := des.NewCipher([]byte("38346591"))
	if err != nil || len(data)%8 != 0 {
		return ""
	}
	decrypted := make([]byte, len(data))
	for i := 0; i < len(data); i += 8 {
		block.Decrypt(decrypted[i:i+8], data[i:i+8])
	}
	if n := len(decrypted); n > 0 {
		pad := int(decrypted[n-1])
		if pad > 0 && pad <= 8 && pad <= n {
			decrypted = decrypted[:n-pad]
		}
	}
	urlStr := strings.TrimSpace(string(decrypted))
	urlStr = strings.Replace(urlStr, "_96.mp4", "_320.mp4", 1)
	urlStr = strings.Replace(urlStr, "_160.mp4", "_320.mp4", 1)
	return urlStr
}

func parseJioImage(raw json.RawMessage) string {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil && str != "" {
		return strings.Replace(strings.Replace(str, "150x150", "500x500", 1), "50x50", "500x500", 1)
	}
	var arr []struct {
		Quality string `json:"quality"`
		URL     string `json:"URL"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return arr[len(arr)-1].URL
	}
	return ""
}

func searchJioSaavn(query string, limit int) []Song {
	if limit <= 0 {
		limit = 25
	}
	apiURL := fmt.Sprintf("%s/search?query=%s&limit=%d", jioAPI, url.QueryEscape(query), limit)
	client := &http.Client{Timeout: 3500 * time.Millisecond}
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Results []struct {
				ID       string          `json:"id"`
				Title    string          `json:"title"`
				Subtitle string          `json:"subtitle"`
				Image    json.RawMessage `json:"image"`
				MoreInfo struct {
					Music    string `json:"music"`
					Duration string `json:"duration"`
				} `json:"more_info"`
			} `json:"results"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return nil
	}

	var songs []Song
	for i, r := range result.Data.Results {
		if i >= limit {
			break
		}
		artist := r.MoreInfo.Music
		if artist == "" {
			parts := strings.SplitN(r.Subtitle, " - ", 2)
			if len(parts) > 0 {
				artist = strings.TrimSpace(parts[0])
			}
		}
		if artist == "" {
			artist = "JioSaavn"
		}
		dur := 180
		if d, err := strconv.Atoi(r.MoreInfo.Duration); err == nil && d > 0 {
			dur = d
		}
		img := parseJioImage(r.Image)

		cleanTitle := html.UnescapeString(r.Title)
		songs = append(songs, Song{
			YTID:     "js-" + r.ID,
			Title:    cleanTitle + " [Jio]",
			Artist:   html.UnescapeString(artist),
			AddedAt:  time.Now().Format(time.RFC3339),
			Duration: dur,
			CoverArt: img,
		})
	}
	return songs
}

func getJioCoverArt(jioID string) string {
	id := strings.TrimPrefix(jioID, "js-")
	apiURL := fmt.Sprintf("%s/songs?id=%s", jioAPI, url.QueryEscape(id))
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Songs []struct {
				Image json.RawMessage `json:"image"`
			} `json:"songs"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) == nil && len(result.Data.Songs) > 0 {
		return parseJioImage(result.Data.Songs[0].Image)
	}
	return ""
}

func getJioStreamURL(jioID string) string {
	id := strings.TrimPrefix(jioID, "js-")
	apiURL := fmt.Sprintf("%s/songs?id=%s", jioAPI, url.QueryEscape(id))
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Songs []struct {
				MoreInfo struct {
					EncryptedMediaURL string `json:"encrypted_media_url"`
					Vlink             string `json:"vlink"`
				} `json:"more_info"`
			} `json:"songs"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil || len(result.Data.Songs) == 0 {
		return ""
	}
	mi := result.Data.Songs[0].MoreInfo
	if u := decryptJioURL(mi.EncryptedMediaURL); u != "" {
		return u
	}
	return mi.Vlink
}

func ensureSongInMemory(s Song) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := appDB.Songs[s.YTID]; !exists {
		appDB.Songs[s.YTID] = s
	}
}

// -------------------- Health Handler --------------------

func healthHandler(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	c := len(appDB.Songs)
	pc := len(appDB.Playlists)
	uc := len(appDB.Users)
	mu.RUnlock()
	cookies := len(getCookiesArg()) > 0
	admin, _ := getAdminCreds()

	fid := latestFileID
	if fid == "" {
		fid = strings.TrimSpace(os.Getenv("DB_JSON_FILE_ID"))
	}
	if fid == "" {
		fid = "Not configured"
	}

	hasAPIKey := strings.TrimSpace(os.Getenv("YOUTUBE_API_KEY")) != ""

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "OK v24-ytapi | Songs: %d | Playlists: %d | Users: %d | Cookies: %v | YT_API: %v | Admin: %s\nDB_JSON_FILE_ID: %s\n", c, pc, uc, cookies, hasAPIKey, admin, fid)
}

// -------------------- Global CORS Middleware --------------------

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// -------------------- App Launcher --------------------

func main() {
	loadDB()
	startTempCleaner()
	startBackgroundSync()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	mux := http.NewServeMux()

	// Subsonic endpoints
	mux.HandleFunc("/rest/", subsonicHandler)

	// Admin, Auth & Utility routes
	mux.HandleFunc("/", healthHandler)
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/list", listHandler)
	mux.HandleFunc("/db", dbHandler)
	mux.HandleFunc("/play", playHandler)
	mux.HandleFunc("/convert", playHandler)
	mux.HandleFunc("/import/youtube-playlist", importPlaylistHandler)
	mux.HandleFunc("/import/playlist", importPlaylistHandler)
	mux.HandleFunc("/playlists/my", myPlaylistsHandler)
	mux.HandleFunc("/playlists/sync", syncSpecificPlaylistHandler)
	mux.HandleFunc("/favorites/my", myFavoritesHandler)
	mux.HandleFunc("/admin/login", adminLoginHandler)
	mux.HandleFunc("/users/register", registerUserHandler)
	mux.HandleFunc("/users/create", registerUserHandler)
	mux.HandleFunc("/users/list", listUsersHandler)
	mux.HandleFunc("/users/delete", deleteUserHandler)

	adminUser, _ := getAdminCreds()
	log.Printf("[Server] Initialized on 0.0.0.0:%s | Admin User: %s", port, adminUser)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, corsMiddleware(mux)))
}
