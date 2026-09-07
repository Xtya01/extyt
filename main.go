package main

import (
	"bytes"
	"crypto/des"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
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

// -------------------- Data Models --------------------

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
}

type User struct {
	Username string            `json:"username"`
	Password string            `json:"password"` // plain for simplicity (10 users)
	Starred  map[string]bool   `json:"starred"`  // songID -> true
	// personal play history overrides
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
	mu  sync.RWMutex
	sem = make(chan struct{}, 1)
)

const dbPath = "/tmp/db.json"
const cookiePath = "/tmp/cookies.txt"

// -------------------- DB --------------------

func loadDB() {
	mu.Lock()
	defer mu.Unlock()

	data, err := os.ReadFile(dbPath)
	if err == nil {
		var newDB AppDB
		if json.Unmarshal(data, &newDB) == nil && newDB.Songs != nil {
			appDB = newDB
			if appDB.Playlists == nil {
				appDB.Playlists = make(map[string]Playlist)
			}
			if appDB.Users == nil {
				appDB.Users = make(map[string]User)
			}
			log.Printf("DB loaded (new format): %d songs, %d playlists", len(appDB.Songs), len(appDB.Playlists))
			return
		}
		var old map[string]Song
		if json.Unmarshal(data, &old) == nil {
			appDB.Songs = old
			appDB.Playlists = make(map[string]Playlist)
			appDB.Users = make(map[string]User)
			log.Printf("DB loaded (old format migrated): %d songs", len(appDB.Songs))
			return
		}
	}

	if fileID := os.Getenv("DB_JSON_FILE_ID"); fileID != "" {
		if err := downloadDBFromTelegram(fileID); err == nil {
			if data, err := os.ReadFile(dbPath); err == nil {
				var newDB AppDB
				if json.Unmarshal(data, &newDB) == nil && newDB.Songs != nil {
					appDB = newDB
					log.Printf("DB restored from Telegram: %d songs", len(appDB.Songs))
					return
				}
			}
		}
	}

	appDB = AppDB{Songs: make(map[string]Song), Playlists: make(map[string]Playlist), Users: make(map[string]User)}
	log.Println("New empty DB")
}

func saveDB() {
	mu.RLock()
	data, _ := json.MarshalIndent(appDB, "", "  ")
	mu.RUnlock()
	os.WriteFile(dbPath, data, 0644)
	go backupDBToTelegram()
}

func backupDBToTelegram() {
	token := os.Getenv("BOT_TOKEN")
	chatID := os.Getenv("CHANNEL_ID")
	if token == "" || chatID == "" {
		return
	}
	file, err := os.Open(dbPath)
	if err != nil {
		return
	}
	defer file.Close()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("document", "db.json")
	io.Copy(part, file)
	writer.WriteField("chat_id", chatID)
	writer.WriteField("caption", fmt.Sprintf("DB backup %s - %d songs - %d playlists", time.Now().Format("2006-01-02 15:04"), len(appDB.Songs), len(appDB.Playlists)))
	writer.Close()
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", token)
	req, _ := http.NewRequest("POST", url, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}
}

func downloadDBFromTelegram(fileID string) error {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return fmt.Errorf("no token")
	}
	resp, err := http.Get(fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", token, fileID))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var gf struct {
		Ok     bool
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&gf)
	if !gf.Ok {
		return fmt.Errorf("getFile fail")
	}
	r2, err := http.Get(fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, gf.Result.FilePath))
	if err != nil {
		return err
	}
	defer r2.Body.Close()
	data, _ := io.ReadAll(r2.Body)
	return os.WriteFile(dbPath, data, 0644)
}

func getTelegramFilePath(fileID string) string {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return ""
	}
	resp, err := http.Get(fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", token, fileID))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var res struct {
		Ok     bool
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&res)
	if res.Ok {
		return res.Result.FilePath
	}
	return ""
}

// -------------------- Cookies --------------------

func getCookiesArg() []string {
	if b64 := os.Getenv("YT_COOKIES_B64"); b64 != "" {
		b64 = strings.TrimSpace(b64)
		b64 = strings.ReplaceAll(b64, "\n", "")
		b64 = strings.ReplaceAll(b64, "\r", "")
		b64 = strings.ReplaceAll(b64, " ", "")
		if decoded, err := base64.StdEncoding.DecodeString(b64); err == nil {
			os.WriteFile(cookiePath, decoded, 0644)
			return []string{"--cookies", cookiePath}
		} else if decoded, err := base64.RawStdEncoding.DecodeString(b64); err == nil {
			os.WriteFile(cookiePath, decoded, 0644)
			return []string{"--cookies", cookiePath}
		}
	}
	if raw := os.Getenv("YT_COOKIES"); raw != "" {
		os.WriteFile(cookiePath, []byte(raw), 0644)
		return []string{"--cookies", cookiePath}
	}
	for _, p := range []string{"/app/cookies.txt", "./cookies.txt", "cookies.txt", cookiePath} {
		if info, err := os.Stat(p); err == nil && info.Size() > 100 {
			return []string{"--cookies", p}
		}
	}
	return []string{}
}

// -------------------- Auth --------------------

func getAdminCreds() (string, string) {
	user := os.Getenv("ADMIN_USER")
	pass := os.Getenv("ADMIN_PASS")
	if user == "" {
		user = "admin"
	}
	if pass == "" {
		pass = "admin"
	}
	return user, pass
}

// getCurrentUser returns username if authenticated, else ""
func getCurrentUser(r *http.Request) string {
	u := r.URL.Query().Get("u")
	p := r.URL.Query().Get("p")
	if u == "" {
		return ""
	}
	// check admin first
	adminUser, adminPass := getAdminCreds()
	if u == adminUser {
		if p == adminPass || strings.HasPrefix(p, "enc:") || (r.URL.Query().Get("t") != "" && r.URL.Query().Get("s") != "") {
			return u
		}
	}
	// check registered users
	mu.RLock()
	defer mu.RUnlock()
	if user, ok := appDB.Users[u]; ok {
		if p == user.Password || strings.HasPrefix(p, "enc:") || (r.URL.Query().Get("t") != "" && r.URL.Query().Get("s") != "") {
			return u
		}
	}
	return ""
}

func checkAuth(r *http.Request) bool {
	return getCurrentUser(r) != ""
}

func isAdmin(r *http.Request) bool {
	u := getCurrentUser(r)
	adminUser, _ := getAdminCreds()
	return u == adminUser
}

// -------------------- Subsonic helpers --------------------

func writeSubsonicError(w http.ResponseWriter, code int, msg string, f string) {
	if f == "json" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"subsonic-response":{"status":"failed","version":"1.16.1","error":{"code":%d,"message":"%s"}}}`, code, msg)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
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
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprint(w, `<subsonic-response status="ok" version="1.16.1" xmlns="http://subsonic.org/restapi">`)
	if s, ok := body.(string); ok {
		fmt.Fprint(w, s)
	}
	fmt.Fprint(w, `</subsonic-response>`)
}

func songToMap(s Song) map[string]interface{} {
	m := map[string]interface{}{
		"id":          s.YTID,
		"title":       s.Title,
		"album":       "Cached Songs",
		"albumId":     "al-1",
		"artist":      s.Artist,
		"artistId":    "ar-1",
		"coverArt":    s.YTID,
		"duration":    s.Duration,
		"bitRate":     128,
		"suffix":      "mp3",
		"contentType": "audio/mpeg",
		"isDir":       false,
		"playCount":   s.PlayCount,
		"created":     s.AddedAt,
		"userRating":  s.Rating,
	}
	if s.Starred {
		m["starred"] = s.LastPlayed
		if s.LastPlayed == "" {
			m["starred"] = s.AddedAt
		}
	}
	if s.LastPlayed != "" {
		m["played"] = s.LastPlayed
	}
	if s.Duration == 0 {
		m["duration"] = 180
	}
	if s.Artist == "" {
		m["artist"] = "YouTube"
	}
	return m
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// -------------------- Title / Search helpers --------------------

func getYTTitle(ytUrl string, cookieArgs []string) (title, artist string) {
	args := []string{
		"--print", "%(title)s|||%(artist)s|||%(uploader)s",
		"--no-download",
		"--no-playlist",
		"--no-check-certificate",
		"--js-runtimes", "deno",
		"--js-runtimes", "node",
		"--extractor-args", "youtube:player_client=web,mweb,android",
	}
	args = append(cookieArgs, args...)
	args = append(args, ytUrl)

	cmd := exec.Command("yt-dlp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", ""
	}
	raw := strings.TrimSpace(string(out))
	lines := strings.Split(raw, "\n")
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

func searchYouTube(query string, maxResults int) []Song {
	if maxResults <= 0 {
		maxResults = 10
	}
	cookieArgs := getCookiesArg()
	searchTerm := fmt.Sprintf("ytsearch%d:%s", maxResults, query)

	args := []string{
		"--print", "%(id)s|||%(title)s|||%(uploader)s",
		"--no-download",
		"--no-playlist",
		"--no-check-certificate",
		"--js-runtimes", "deno",
		"--js-runtimes", "node",
		"--extractor-args", "youtube:player_client=web,mweb,android",
	}
	args = append(cookieArgs, args...)
	args = append(args, searchTerm)

	cmd := exec.Command("yt-dlp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("YouTube search failed: %v", err)
		return nil
	}

	var results []Song
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
			YTID:    id,
			Title:   title,
			Artist:  artist,
			AddedAt: time.Now().Format(time.RFC3339),
		})
	}
	return results
}

// -------------------- Handlers --------------------

func health(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	c := len(appDB.Songs)
	pc := len(appDB.Playlists)
	mu.RUnlock()
	cookies := len(getCookiesArg()) > 0
	user, _ := getAdminCreds()
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "OK v11-phase1 - %d songs - %d playlists - cookies: %v - admin: %s\n", c, pc, cookies, user)
}

func listHandler(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	// Return only songs map for Admin UI compatibility
	json.NewEncoder(w).Encode(appDB.Songs)
}

func dbHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, dbPath)
}

func playHandler(w http.ResponseWriter, r *http.Request) {
	ytUrl := r.URL.Query().Get("url")
	if ytUrl == "" {
		http.Error(w, "use /play?url=YT_URL", 400)
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
	if song, ok := appDB.Songs[ytID]; ok && song.FileID != "" {
		mu.RUnlock()
		token := os.Getenv("BOT_TOKEN")
		fp := song.FilePath
		if fp == "" {
			fp = getTelegramFilePath(song.FileID)
		}
		if token != "" && fp != "" {
			go func() {
				mu.Lock()
				if s, ok := appDB.Songs[ytID]; ok {
					s.PlayCount++
					s.LastPlayed = time.Now().Format(time.RFC3339)
					appDB.Songs[ytID] = s
					mu.Unlock()
					saveDB()
				} else {
					mu.Unlock()
				}
			}()
			http.Redirect(w, r, fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, fp), 302)
			return
		}
	}
	mu.RUnlock()

	sem <- struct{}{}
	defer func() { <-sem }()

	tmpFile := filepath.Join("/tmp", ytID+".mp3")
	os.Remove(tmpFile)

	cookieArgs := getCookiesArg()
	title, artist := getYTTitle(ytUrl, cookieArgs)
	if title == "" {
		title = ytID
	}
	if artist == "" {
		artist = "YouTube"
	}

	ytArgs := []string{
		"-x", "--audio-format", "mp3",
		"--no-playlist",
		"--no-check-certificate",
		"--js-runtimes", "deno",
		"--js-runtimes", "node",
		"--extractor-args", "youtube:player_client=web,mweb,android",
		"-o", tmpFile,
	}
	ytArgs = append(cookieArgs, ytArgs...)
	ytArgs = append(ytArgs, ytUrl)

	cmd := exec.Command("yt-dlp", ytArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmpFile)
		http.Error(w, fmt.Sprintf("yt-dlp failed: %v\n%s", err, string(out)), 500)
		return
	}

	now := time.Now().Format(time.RFC3339)
	token := os.Getenv("BOT_TOKEN")
	chatID := os.Getenv("CHANNEL_ID")
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
			if resp, err := http.DefaultClient.Do(req); err == nil {
				defer resp.Body.Close()
				var res struct {
					Ok     bool `json:"ok"`
					Result struct {
						Audio struct {
							FileID string `json:"file_id"`
						} `json:"audio"`
					} `json:"result"`
				}
				json.NewDecoder(resp.Body).Decode(&res)
				if res.Ok {
					fileID = res.Result.Audio.FileID
					fp = getTelegramFilePath(fileID)
				}
			}
		}
	}

	mu.Lock()
	existing := appDB.Songs[ytID]
	s := Song{
		YTID: ytID, Title: title, Artist: artist,
		FileID: fileID, FilePath: fp,
		AddedAt: existing.AddedAt, PlayCount: existing.PlayCount + 1,
		LastPlayed: now, Starred: existing.Starred, Rating: existing.Rating,
		Duration: existing.Duration,
	}
	if s.AddedAt == "" {
		s.AddedAt = now
	}
	appDB.Songs[ytID] = s
	mu.Unlock()
	saveDB()

	w.Header().Set("Content-Type", "audio/mpeg")
	http.ServeFile(w, r, tmpFile)
	go func() {
		time.Sleep(45 * time.Second)
		os.Remove(tmpFile)
	}()
}

func updateTitlesHandler(w http.ResponseWriter, r *http.Request) {
	user, pass := getAdminCreds()
	u := r.URL.Query().Get("u")
	p := r.URL.Query().Get("p")
	if (u != user || p != pass) && r.URL.Query().Get("key") != pass {
		http.Error(w, "Unauthorized", 401)
		return
	}

	mu.RLock()
	ids := make([]string, 0)
	for id, s := range appDB.Songs {
		if s.Title == "" || s.Title == id || (len(s.Title) == 11 && !strings.Contains(s.Title, " ")) {
			ids = append(ids, id)
		}
	}
	mu.RUnlock()

	updated := 0
	cookieArgs := getCookiesArg()
	for _, id := range ids {
		title, artist := getYTTitle("https://www.youtube.com/watch?v="+id, cookieArgs)
		if title != "" && title != id {
			mu.Lock()
			if s, ok := appDB.Songs[id]; ok {
				s.Title = title
				if artist != "" {
					s.Artist = artist
				}
				appDB.Songs[id] = s
				updated++
			}
			mu.Unlock()
		}
		time.Sleep(700 * time.Millisecond)
	}
	if updated > 0 {
		saveDB()
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","checked":%d,"updated":%d}`, len(ids), updated)
}

// -------------------- Subsonic --------------------

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

	switch path {
	case "ping":
		writeSubsonicOK(w, f, map[string]interface{}{})
	case "getlicense":
		writeSubsonicOK(w, f, map[string]interface{}{"license": map[string]interface{}{"valid": true, "email": "admin@local", "licenseExpires": "2099-01-01T00:00:00"}})
	case "getmusicfolders":
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"musicFolders": map[string]interface{}{"musicFolder": []map[string]interface{}{{"id": 1, "name": "YouTube Cache"}}}})
		} else {
			writeSubsonicOK(w, f, `<musicFolders><musicFolder id="1" name="YouTube Cache"/></musicFolders>`)
		}
	case "getindexes", "getartists":
		// Group by real artist name
		mu.RLock()
		artistMap := make(map[string]int) // artist -> song count
		for _, s := range appDB.Songs {
			a := s.Artist
			if a == "" {
				a = "YouTube"
			}
			artistMap[a]++
		}
		mu.RUnlock()

		// Build index by first letter
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

		// sort letters
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
		if r.URL.Query().Get("id") != "ar-1" {
			writeSubsonicError(w, 70, "Artist not found", f)
			return
		}
		mu.RLock()
		defer mu.RUnlock()
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"artist": map[string]interface{}{"id": "ar-1", "name": "YouTube", "albumCount": 1, "album": []map[string]interface{}{{"id": "al-1", "name": "Cached Songs", "artist": "YouTube", "artistId": "ar-1", "songCount": len(appDB.Songs), "coverArt": "al-1"}}}})
		} else {
			writeSubsonicOK(w, f, fmt.Sprintf(`<artist id="ar-1" name="YouTube" albumCount="1"><album id="al-1" name="Cached Songs" artist="YouTube" artistId="ar-1" songCount="%d"/></artist>`, len(appDB.Songs)))
		}
	case "getalbum", "getmusicdirectory":
		id := r.URL.Query().Get("id")
		mu.RLock()
		defer mu.RUnlock()
		if id == "al-1" || id == "ar-1" || id == "1" {
			children := []map[string]interface{}{}
			for _, s := range appDB.Songs {
				children = append(children, songToMap(s))
			}
			if f == "json" {
				writeSubsonicOK(w, f, map[string]interface{}{"album": map[string]interface{}{"id": "al-1", "name": "Cached Songs", "artist": "YouTube", "artistId": "ar-1", "songCount": len(appDB.Songs), "coverArt": "al-1", "song": children}, "directory": map[string]interface{}{"id": "al-1", "name": "Cached Songs", "child": children}})
			} else {
				var b strings.Builder
				b.WriteString(fmt.Sprintf(`<album id="al-1" name="Cached Songs" artist="YouTube" artistId="ar-1" songCount="%d">`, len(appDB.Songs)))
				for _, s := range appDB.Songs {
					b.WriteString(fmt.Sprintf(`<song id="%s" title="%s" artist="%s" coverArt="%s" duration="%d" playCount="%d"/>`, s.YTID, xmlEscape(s.Title), xmlEscape(s.Artist), s.YTID, s.Duration, s.PlayCount))
				}
				b.WriteString(`</album>`)
				writeSubsonicOK(w, f, b.String())
			}
		} else {
			writeSubsonicError(w, 70, "Not found", f)
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
			writeSubsonicOK(w, f, map[string]interface{}{"song": songToMap(song)})
		} else {
			writeSubsonicOK(w, f, fmt.Sprintf(`<song id="%s" title="%s" artist="%s" coverArt="%s" duration="%d" playCount="%d"/>`, song.YTID, xmlEscape(song.Title), xmlEscape(song.Artist), song.YTID, song.Duration, song.PlayCount))
		}
	case "stream", "download":
		id := r.URL.Query().Get("id")
		if id == "" {
			writeSubsonicError(w, 10, "Missing id", f)
			return
		}
		// update play stats
		go func(sid string) {
			mu.Lock()
			if s, ok := appDB.Songs[sid]; ok {
				s.PlayCount++
				s.LastPlayed = time.Now().Format(time.RFC3339)
				appDB.Songs[sid] = s
				mu.Unlock()
				saveDB()
			} else {
				mu.Unlock()
			}
		}(id)

		// JioSaavn song?
		if strings.HasPrefix(id, "js-") {
			streamURL := getJioStreamURL(id)
			if streamURL == "" {
				writeSubsonicError(w, 70, "JioSaavn stream not available", f)
				return
			}
			http.Redirect(w, r, streamURL, 302)
			return
		}

		// YouTube
		newURL := *r.URL
		q := newURL.Query()
		q.Set("url", "https://www.youtube.com/watch?v="+id)
		newURL.RawQuery = q.Encode()
		r.URL = &newURL
		playHandler(w, r)
	case "getcoverart":
		id := r.URL.Query().Get("id")
		if len(id) >= 11 {
			if len(id) > 11 {
				id = id[len(id)-11:]
			}
			http.Redirect(w, r, "https://i.ytimg.com/vi/"+id+"/hqdefault.jpg", 302)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d, 0xb0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82})
	case "search2", "search3":
		q := strings.TrimSpace(r.URL.Query().Get("query"))
		qLower := strings.ToLower(q)
		mu.RLock()
		var matched []Song
		for _, s := range appDB.Songs {
			if qLower == "" || strings.Contains(strings.ToLower(s.Title), qLower) || strings.Contains(strings.ToLower(s.Artist), qLower) || strings.Contains(strings.ToLower(s.YTID), qLower) {
				matched = append(matched, s)
			}
		}
		mu.RUnlock()
		if len(matched) < 12 && len(q) >= 2 {
			seen := map[string]bool{}
			for _, s := range matched {
				seen[s.YTID] = true
			}
			// YouTube search
			ytResults := searchYouTube(q, 8)
			for _, ys := range ytResults {
				if !seen[ys.YTID] {
					matched = append(matched, ys)
					seen[ys.YTID] = true
				}
			}
			// JioSaavn search
			jioResults := searchJioSaavn(q, 8)
			for _, js := range jioResults {
				if !seen[js.YTID] {
					matched = append(matched, js)
					seen[js.YTID] = true
					// save metadata so stream works later
					go ensureJioSongInDB(js)
				}
			}
		}
		songs := []map[string]interface{}{}
		for _, s := range matched {
			songs = append(songs, songToMap(s))
		}
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"searchResult2": map[string]interface{}{"song": songs}, "searchResult3": map[string]interface{}{"song": songs}})
		} else {
			var b strings.Builder
			b.WriteString(`<searchResult3>`)
			for _, s := range matched {
				b.WriteString(fmt.Sprintf(`<song id="%s" title="%s" artist="%s" coverArt="%s"/>`, s.YTID, xmlEscape(s.Title), xmlEscape(s.Artist), s.YTID))
			}
			b.WriteString(`</searchResult3>`)
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
				if !s.Starred {
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
			albums = append(albums, map[string]interface{}{"id": "al-" + it.s.YTID, "name": it.s.Title, "artist": it.s.Artist, "artistId": "ar-1", "coverArt": it.s.YTID, "songCount": 1, "created": it.s.AddedAt, "playCount": it.s.PlayCount})
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
			out = append(out, songToMap(s))
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
		username := getCurrentUser(r)
		mu.Lock()
		if user, ok := appDB.Users[username]; ok {
			if user.Starred == nil {
				user.Starred = make(map[string]bool)
			}
			user.Starred[id] = true
			appDB.Users[username] = user
		} else if isAdmin(r) {
			// admin can also star on global for backward compat
			if s, ok := appDB.Songs[id]; ok {
				s.Starred = true
				appDB.Songs[id] = s
			}
		}
		mu.Unlock()
		saveDB()
		writeSubsonicOK(w, f, map[string]interface{}{})
	case "unstar":
		id := r.URL.Query().Get("id")
		username := getCurrentUser(r)
		mu.Lock()
		if user, ok := appDB.Users[username]; ok {
			if user.Starred != nil {
				delete(user.Starred, id)
				appDB.Users[username] = user
			}
		} else if isAdmin(r) {
			if s, ok := appDB.Songs[id]; ok {
				s.Starred = false
				appDB.Songs[id] = s
			}
		}
		mu.Unlock()
		saveDB()
		writeSubsonicOK(w, f, map[string]interface{}{})
	case "getstarred", "getstarred2":
		username := getCurrentUser(r)
		var starred []map[string]interface{}
		mu.RLock()
		if user, ok := appDB.Users[username]; ok && user.Starred != nil {
			for sid := range user.Starred {
				if s, ok := appDB.Songs[sid]; ok {
					starred = append(starred, songToMap(s))
				}
			}
		} else {
			// fallback global
			for _, s := range appDB.Songs {
				if s.Starred {
					starred = append(starred, songToMap(s))
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
				mu.Unlock()
				saveDB()
			} else {
				mu.Unlock()
			}
		}
		writeSubsonicOK(w, f, map[string]interface{}{})
	case "getplaylists":
		username := getCurrentUser(r)
		mu.RLock()
		var pls []map[string]interface{}
		for _, p := range appDB.Playlists {
			// show own playlists + public ones + admin sees all
			if p.Owner == username || p.Public || isAdmin(r) || p.Owner == "admin" {
				pls = append(pls, map[string]interface{}{"id": p.ID, "name": p.Name, "songCount": len(p.SongIDs), "created": p.Created, "changed": p.Changed, "owner": p.Owner, "public": p.Public})
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
		mu.RUnlock()
		if !ok {
			writeSubsonicError(w, 70, "Playlist not found", f)
			return
		}
		var songs []map[string]interface{}
		mu.RLock()
		for _, sid := range pl.SongIDs {
			if s, ok := appDB.Songs[sid]; ok {
				songs = append(songs, songToMap(s))
			}
		}
		mu.RUnlock()
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{"playlist": map[string]interface{}{"id": pl.ID, "name": pl.Name, "songCount": len(pl.SongIDs), "entry": songs}})
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
		var ids []string
		for _, v := range r.URL.Query()["songId"] {
			ids = append(ids, v)
		}
		owner := getCurrentUser(r)
		if owner == "" { owner = "admin" }
		pl := Playlist{ID: id, Name: name, SongIDs: ids, Created: now, Changed: now, Owner: owner, Public: false}
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
		delete(appDB.Playlists, id)
		mu.Unlock()
		saveDB()
		writeSubsonicOK(w, f, map[string]interface{}{})
	case "getlyrics":
		artist := r.URL.Query().Get("artist")
		title := r.URL.Query().Get("title")
		// also try by song id if provided
		id := r.URL.Query().Get("id")
		var lyrics string
		if id != "" {
			lyrics = getLyricsForSong(id)
		} else {
			lyrics = fetchLyrics(artist, title)
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
		if id == "" {
			writeSubsonicError(w, 10, "Missing id", f)
			return
		}
		lyrics := getLyricsForSong(id)
		mu.RLock()
		s := appDB.Songs[id]
		mu.RUnlock()
		if f == "json" {
			// OpenSubsonic style
			writeSubsonicOK(w, f, map[string]interface{}{
				"lyricsList": map[string]interface{}{
					"structuredLyrics": []map[string]interface{}{
						{
							"lang": "en",
							"displayArtist": s.Artist,
							"displayTitle":  s.Title,
							"offset": 0,
							"synced": false,
							"line": []map[string]interface{}{
								{"value": lyrics},
							},
						},
					},
				},
				"lyrics": map[string]interface{}{
					"artist": s.Artist,
					"title":  s.Title,
					"value":  lyrics,
				},
			})
		} else {
			writeSubsonicOK(w, f, fmt.Sprintf(`<lyrics artist="%s" title="%s">%s</lyrics>`, xmlEscape(s.Artist), xmlEscape(s.Title), xmlEscape(lyrics)))
		}

	default:
		writeSubsonicError(w, 0, "Not implemented: "+path, f)
	}
}


// ==================== PHASE 2: YouTube Playlist Import ====================

func importYouTubePlaylist(playlistURL string, maxVideos int) (int, error) {
	if maxVideos <= 0 {
		maxVideos = 50
	}
	cookieArgs := getCookiesArg()
	args := []string{
		"--flat-playlist",
		"--print", "%(id)s|||%(title)s|||%(uploader)s",
		"--no-download",
		"--playlist-end", fmt.Sprintf("%d", maxVideos),
		"--js-runtimes", "deno",
		"--js-runtimes", "node",
		"--extractor-args", "youtube:player_client=web,mweb,android",
	}
	args = append(cookieArgs, args...)
	args = append(args, playlistURL)

	cmd := exec.Command("yt-dlp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("yt-dlp playlist failed: %v\n%s", err, string(out))
	}

	added := 0
	now := time.Now().Format(time.RFC3339)
	mu.Lock()
	defer mu.Unlock()

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
		if _, exists := appDB.Songs[id]; !exists {
			appDB.Songs[id] = Song{
				YTID:    id,
				Title:   title,
				Artist:  artist,
				AddedAt: now,
			}
			added++
		}
	}
	return added, nil
}

func importPlaylistHandler(w http.ResponseWriter, r *http.Request) {
	user, pass := getAdminCreds()
	u := r.URL.Query().Get("u")
	p := r.URL.Query().Get("p")
	if (u != user || p != pass) && r.URL.Query().Get("key") != pass {
		http.Error(w, "Unauthorized", 401)
		return
	}

	playlistURL := r.URL.Query().Get("url")
	if playlistURL == "" {
		http.Error(w, "missing url parameter (YouTube playlist URL)", 400)
		return
	}
	maxV := 50
	if maxStr := r.URL.Query().Get("max"); maxStr != "" {
		if v, err := strconv.Atoi(maxStr); err == nil && v > 0 {
			maxV = v
		}
	}

	added, err := importYouTubePlaylist(playlistURL, maxV)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	saveDB()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","added":%d,"message":"Playlist imported. Songs will download when played."}`, added)
}



// ==================== MULTI-USER MANAGEMENT ====================

func createUserHandler(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) && getCurrentUser(r) == "" {
		// allow with admin creds in query
		adminU, adminP := getAdminCreds()
		if r.URL.Query().Get("u") != adminU || r.URL.Query().Get("p") != adminP {
			http.Error(w, "Admin only", 403)
			return
		}
	} else if !isAdmin(r) {
		http.Error(w, "Admin only", 403)
		return
	}

	username := strings.TrimSpace(r.URL.Query().Get("username"))
	password := r.URL.Query().Get("password")
	if username == "" || password == "" {
		http.Error(w, "username and password required", 400)
		return
	}

	mu.Lock()
	if _, exists := appDB.Users[username]; exists {
		mu.Unlock()
		http.Error(w, "User already exists", 400)
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
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","username":"%s"}`, username)
}

func listUsersHandler(w http.ResponseWriter, r *http.Request) {
	adminU, adminP := getAdminCreds()
	if r.URL.Query().Get("u") != adminU || r.URL.Query().Get("p") != adminP {
		http.Error(w, "Admin only", 403)
		return
	}
	mu.RLock()
	defer mu.RUnlock()
	type uinfo struct {
		Username string `json:"username"`
		Stars    int    `json:"stars"`
	}
	var list []uinfo
	for _, u := range appDB.Users {
		list = append(list, uinfo{Username: u.Username, Stars: len(u.Starred)})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"users": list, "admin": adminU})
}

func deleteUserHandler(w http.ResponseWriter, r *http.Request) {
	adminU, adminP := getAdminCreds()
	if r.URL.Query().Get("u") != adminU || r.URL.Query().Get("p") != adminP {
		http.Error(w, "Admin only", 403)
		return
	}
	username := r.URL.Query().Get("username")
	if username == "" {
		http.Error(w, "username required", 400)
		return
	}
	mu.Lock()
	delete(appDB.Users, username)
	mu.Unlock()
	saveDB()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","deleted":"%s"}`, username)
}



// ==================== ADMIN LOGIN (for CF Worker UI) ====================

func adminLoginHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	if r.Method == "OPTIONS" {
		w.WriteHeader(204)
		return
	}

	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}

	var body struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// also try form
		body.User = r.FormValue("user")
		body.Pass = r.FormValue("pass")
	}

	user := strings.TrimSpace(body.User)
	pass := body.Pass
	if user == "" {
		user = "admin"
	}

	ok := false
	// check admin
	adminUser, adminPass := getAdminCreds()
	if user == adminUser && pass == adminPass {
		ok = true
	}
	// check registered users
	if !ok {
		mu.RLock()
		if u, exists := appDB.Users[user]; exists && u.Password == pass {
			ok = true
		}
		mu.RUnlock()
	}

	w.Header().Set("Content-Type", "application/json")
	if ok {
		// simple token (UI just stores it)
		token := fmt.Sprintf("tok_%s_%d", user, time.Now().Unix())
		fmt.Fprintf(w, `{"ok":true,"token":"%s","user":"%s"}`, token, user)
	} else {
		fmt.Fprintf(w, `{"ok":false,"error":"Wrong username or password"}`)
	}
}



// ==================== YOUTUBE MUSIC AUTO FETCH ====================

// syncYouTubeMusic imports from configured YT Music / YT playlists
// ENV: YTMUSIC_PLAYLISTS = comma separated playlist URLs
// Also supports music.youtube.com links
func syncYouTubeMusicHandler(w http.ResponseWriter, r *http.Request) {
	user, pass := getAdminCreds()
	u := r.URL.Query().Get("u")
	p := r.URL.Query().Get("p")
	if (u != user || p != pass) && r.URL.Query().Get("key") != pass {
		// also allow current multi-user admin
		if !isAdmin(r) {
			http.Error(w, "Admin only", 403)
			return
		}
	}

	// 1. From query param
	urls := []string{}
	if q := r.URL.Query().Get("url"); q != "" {
		urls = append(urls, q)
	}
	// 2. From ENV (comma separated)
	if env := os.Getenv("YTMUSIC_PLAYLISTS"); env != "" {
		for _, u := range strings.Split(env, ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				urls = append(urls, u)
			}
		}
	}
	// 3. Default popular YT Music mix if nothing given and library empty
	mu.RLock()
	empty := len(appDB.Songs) == 0
	mu.RUnlock()
	if len(urls) == 0 && empty {
		// Some public-ish starting points (user can override with ENV)
		urls = []string{
			"https://www.youtube.com/playlist?list=PL4fGSI1pDJn6puJdseH2Rt9sMvtgENekE", // popular
		}
	}

	if len(urls) == 0 {
		http.Error(w, "No playlist URL. Pass ?url=... or set YTMUSIC_PLAYLISTS ENV", 400)
		return
	}

	maxV := 30
	if m := r.URL.Query().Get("max"); m != "" {
		if v, err := strconv.Atoi(m); err == nil && v > 0 {
			maxV = v
		}
	}

	totalAdded := 0
	var details []string
	for _, plURL := range urls {
		added, err := importYouTubePlaylist(plURL, maxV)
		if err != nil {
			details = append(details, fmt.Sprintf("%s → error: %v", plURL, err))
			continue
		}
		totalAdded += added
		details = append(details, fmt.Sprintf("%s → +%d songs", plURL, added))
	}
	if totalAdded > 0 {
		saveDB()
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","added":%d,"details":%s}`, totalAdded, mustJSON(details))
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// autoSyncOnStartup runs once if library is empty and YTMUSIC_PLAYLISTS is set
func autoSyncOnStartup() {
	mu.RLock()
	empty := len(appDB.Songs) == 0
	mu.RUnlock()
	if !empty {
		return
	}
	env := os.Getenv("YTMUSIC_PLAYLISTS")
	if env == "" {
		log.Println("Library empty. Set YTMUSIC_PLAYLISTS ENV or call /library/sync to auto-fetch.")
		return
	}
	log.Println("Library empty → auto syncing YouTube Music playlists...")
	for _, u := range strings.Split(env, ",") {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		added, err := importYouTubePlaylist(u, 40)
		if err != nil {
			log.Printf("Auto-sync failed for %s: %v", u, err)
			continue
		}
		log.Printf("Auto-sync: %s → +%d songs", u, added)
	}
	saveDB()
}



// ==================== LYRICS SYSTEM ====================

func fetchLyrics(artist, title string) string {
	if title == "" {
		return ""
	}
	// Clean title (remove (Official Video) etc)
	clean := title
	for _, cut := range []string{"(Official Video)", "(Official Audio)", "(Lyrics)", "(Audio)", "[Official Video]", "|", " - Topic"} {
		clean = strings.ReplaceAll(clean, cut, "")
	}
	clean = strings.TrimSpace(clean)

	// Try lyrics.ovh first (simple, free)
	url1 := fmt.Sprintf("https://api.lyrics.ovh/v1/%s/%s", url.PathEscape(artist), url.PathEscape(clean))
	if artist == "" || artist == "YouTube" {
		// try with title only split
		parts := strings.SplitN(clean, " - ", 2)
		if len(parts) == 2 {
			url1 = fmt.Sprintf("https://api.lyrics.ovh/v1/%s/%s", url.PathEscape(strings.TrimSpace(parts[0])), url.PathEscape(strings.TrimSpace(parts[1])))
		} else {
			url1 = fmt.Sprintf("https://api.lyrics.ovh/v1/%s/%s", url.PathEscape("Unknown"), url.PathEscape(clean))
		}
	}

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(url1)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			var data struct {
				Lyrics string `json:"lyrics"`
			}
			if json.NewDecoder(resp.Body).Decode(&data) == nil && strings.TrimSpace(data.Lyrics) != "" {
				return strings.TrimSpace(data.Lyrics)
			}
		}
	}

	// Fallback: lrclib.net
	q := url.QueryEscape(clean)
	if artist != "" && artist != "YouTube" {
		q = url.QueryEscape(artist + " " + clean)
	}
	url2 := "https://lrclib.net/api/search?q=" + q
	resp2, err := client.Get(url2)
	if err == nil {
		defer resp2.Body.Close()
		if resp2.StatusCode == 200 {
			var results []struct {
				PlainLyrics string `json:"plainLyrics"`
				SyncedLyrics string `json:"syncedLyrics"`
				TrackName   string `json:"trackName"`
				ArtistName  string `json:"artistName"`
			}
			if json.NewDecoder(resp2.Body).Decode(&results) == nil && len(results) > 0 {
				if results[0].PlainLyrics != "" {
					return strings.TrimSpace(results[0].PlainLyrics)
				}
				if results[0].SyncedLyrics != "" {
					// strip LRC timestamps roughly
					lines := strings.Split(results[0].SyncedLyrics, "\n")
					var plain []string
					for _, l := range lines {
						if idx := strings.Index(l, "]"); idx != -1 && idx < 12 {
							l = strings.TrimSpace(l[idx+1:])
						}
						if l != "" {
							plain = append(plain, l)
						}
					}
					return strings.Join(plain, "\n")
				}
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
	// fetch and cache
	lyrics := fetchLyrics(s.Artist, s.Title)
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



// ==================== JIOSAAVN INTEGRATION ====================

const jioAPI = "https://jiosaavn-api-three-ashy.vercel.app"

func decryptJioURL(encrypted string) string {
	if encrypted == "" {
		return ""
	}
	// pad base64
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
	if err != nil {
		return ""
	}
	if len(data)%8 != 0 {
		return ""
	}
	decrypted := make([]byte, len(data))
	for i := 0; i < len(data); i += 8 {
		block.Decrypt(decrypted[i:i+8], data[i:i+8])
	}
	// PKCS5 unpad
	if n := len(decrypted); n > 0 {
		pad := int(decrypted[n-1])
		if pad > 0 && pad <= 8 && pad <= n {
			decrypted = decrypted[:n-pad]
		}
	}
	urlStr := strings.TrimSpace(string(decrypted))
	// prefer 320kbps
	urlStr = strings.Replace(urlStr, "_96.mp4", "_320.mp4", 1)
	urlStr = strings.Replace(urlStr, "_160.mp4", "_320.mp4", 1)
	return urlStr
}

func searchJioSaavn(query string, limit int) []Song {
	if limit <= 0 {
		limit = 10
	}
	apiURL := fmt.Sprintf("%s/search?query=%s", jioAPI, url.QueryEscape(query))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		log.Printf("JioSaavn search error: %v", err)
		return nil
	}
	defer resp.Body.Close()
	var result struct {
		Status string `json:"status"`
		Data   struct {
			Results []struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Subtitle string `json:"subtitle"`
				Image    string `json:"image"`
				MoreInfo struct {
					Album    string `json:"album"`
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
			// subtitle often "Artist - Album"
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
		songs = append(songs, Song{
			YTID:     "js-" + r.ID,
			Title:    r.Title,
			Artist:   artist,
			AddedAt:  time.Now().Format(time.RFC3339),
			Duration: dur,
		})
	}
	return songs
}

func getJioStreamURL(jioID string) string {
	// strip js- prefix if present
	id := strings.TrimPrefix(jioID, "js-")
	apiURL := fmt.Sprintf("%s/songs?id=%s", jioAPI, url.QueryEscape(id))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var result struct {
		Status string `json:"status"`
		Data   struct {
			Songs []struct {
				MoreInfo struct {
					EncryptedMediaURL string `json:"encrypted_media_url"`
					Vlink             string `json:"vlink"`
					Duration          string `json:"duration"`
				} `json:"more_info"`
				Title string `json:"title"`
			} `json:"songs"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return ""
	}
	if len(result.Data.Songs) == 0 {
		return ""
	}
	mi := result.Data.Songs[0].MoreInfo
	if u := decryptJioURL(mi.EncryptedMediaURL); u != "" {
		return u
	}
	// fallback preview
	return mi.Vlink
}

func ensureJioSongInDB(s Song) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := appDB.Songs[s.YTID]; !exists {
		appDB.Songs[s.YTID] = s
	}
}


func main() {
	loadDB()
	go autoSyncOnStartup()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	http.HandleFunc("/", health)
	http.HandleFunc("/health", health)
	http.HandleFunc("/list", listHandler)
	http.HandleFunc("/db", dbHandler)
	http.HandleFunc("/play", playHandler)
	http.HandleFunc("/convert", playHandler)
	http.HandleFunc("/update-titles", updateTitlesHandler)
	http.HandleFunc("/import/youtube-playlist", importPlaylistHandler)
	http.HandleFunc("/users/create", createUserHandler)
	http.HandleFunc("/users/list", listUsersHandler)
	http.HandleFunc("/users/delete", deleteUserHandler)
	http.HandleFunc("/admin/login", adminLoginHandler)
	http.HandleFunc("/library/sync", syncYouTubeMusicHandler)
	http.HandleFunc("/import/ytmusic", syncYouTubeMusicHandler)
	http.HandleFunc("/rest/", subsonicHandler)
	user, _ := getAdminCreds()
	log.Printf("Listening on 0.0.0.0:%s | Phase-1 ready | Admin: %s", port, user)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}
