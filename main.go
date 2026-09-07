package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Song struct {
	YTID     string `json:"yt_id"`
	Title    string `json:"title"`
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
	AddedAt  string `json:"added_at"`
}

var (
	db  = make(map[string]Song)
	mu  sync.RWMutex
	sem = make(chan struct{}, 1)
)

const dbPath = "/tmp/db.json"
const cookiePath = "/tmp/cookies.txt"

// -------------------- DB --------------------

func loadDB() {
	mu.Lock()
	defer mu.Unlock()
	if data, err := os.ReadFile(dbPath); err == nil {
		json.Unmarshal(data, &db)
		log.Printf("DB loaded local: %d songs", len(db))
		return
	}
	if fileID := os.Getenv("DB_JSON_FILE_ID"); fileID != "" {
		if err := downloadDBFromTelegram(fileID); err == nil {
			if data, err := os.ReadFile(dbPath); err == nil {
				json.Unmarshal(data, &db)
				log.Printf("DB restored from Telegram: %d songs", len(db))
				return
			}
		}
	}
	db = make(map[string]Song)
	log.Println("New empty DB")
}

func saveDB() {
	mu.RLock()
	data, _ := json.MarshalIndent(db, "", "  ")
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
	writer.WriteField("caption", fmt.Sprintf("DB backup %s - %d songs", time.Now().Format("2006-01-02 15:04"), len(db)))
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

func checkAuth(r *http.Request) bool {
	user, pass := getAdminCreds()
	u := r.URL.Query().Get("u")
	p := r.URL.Query().Get("p")
	// token auth support (simple)
	t := r.URL.Query().Get("t")
	s := r.URL.Query().Get("s")
	if u == "" {
		return false
	}
	if p != "" {
		// plain or enc:hex
		if strings.HasPrefix(p, "enc:") {
			// very basic, just accept if user matches for now
			return u == user
		}
		return u == user && p == pass
	}
	if t != "" && s != "" {
		// accept any token for simplicity (most clients use password)
		return u == user
	}
	return false
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
		// merge body
		if m, ok := body.(map[string]interface{}); ok {
			for k, v := range m {
				resp["subsonic-response"].(map[string]interface{})[k] = v
			}
		}
		json.NewEncoder(w).Encode(resp)
		return
	}
	// XML
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprint(w, `<subsonic-response status="ok" version="1.16.1" xmlns="http://subsonic.org/restapi">`)
	if s, ok := body.(string); ok {
		fmt.Fprint(w, s)
	}
	fmt.Fprint(w, `</subsonic-response>`)
}

// -------------------- Original handlers --------------------

func health(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	c := len(db)
	mu.RUnlock()
	cookies := len(getCookiesArg()) > 0
	user, _ := getAdminCreds()
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "OK v10-subsonic - %d songs - cookies: %v - admin: %s\n", c, cookies, user)
}

func listHandler(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(db)
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

	// Cached?
	mu.RLock()
	if song, ok := db[ytID]; ok && song.FileID != "" {
		mu.RUnlock()
		token := os.Getenv("BOT_TOKEN")
		fp := song.FilePath
		if fp == "" {
			fp = getTelegramFilePath(song.FileID)
		}
		if token != "" && fp != "" {
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

	// Upload Telegram
	token := os.Getenv("BOT_TOKEN")
	chatID := os.Getenv("CHANNEL_ID")
	title := ytID
	if token != "" && chatID != "" {
		if f, err := os.Open(tmpFile); err == nil {
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)
			part, _ := writer.CreateFormFile("audio", filepath.Base(tmpFile))
			io.Copy(part, f)
			f.Close()
			writer.WriteField("chat_id", chatID)
			writer.WriteField("caption", ytID)
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
					fp := getTelegramFilePath(res.Result.Audio.FileID)
					mu.Lock()
					db[ytID] = Song{YTID: ytID, Title: title, FileID: res.Result.Audio.FileID, FilePath: fp, AddedAt: time.Now().Format(time.RFC3339)}
					mu.Unlock()
					saveDB()
				}
			}
		}
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	http.ServeFile(w, r, tmpFile)
	go func() {
		time.Sleep(45 * time.Second)
		os.Remove(tmpFile)
	}()
}

// -------------------- Subsonic Handlers --------------------

func subsonicHandler(w http.ResponseWriter, r *http.Request) {
	// /rest/xxxxx.view
	path := strings.TrimPrefix(r.URL.Path, "/rest/")
	path = strings.TrimSuffix(path, ".view")
	path = strings.ToLower(path)

	f := r.URL.Query().Get("f")
	if f == "" {
		f = "xml"
	}

	// Auth check (except ping sometimes, but we check all)
	if !checkAuth(r) && path != "ping" {
		// still allow ping without auth for discovery
		if path != "ping" {
			writeSubsonicError(w, 40, "Wrong username or password", f)
			return
		}
	}

	switch path {
	case "ping":
		writeSubsonicOK(w, f, map[string]interface{}{})
	case "getlicense":
		writeSubsonicOK(w, f, map[string]interface{}{
			"license": map[string]interface{}{"valid": true, "email": "admin@local", "licenseExpires": "2099-01-01T00:00:00"},
		})
	case "getmusicfolders":
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"musicFolders": map[string]interface{}{
					"musicFolder": []map[string]interface{}{{"id": 1, "name": "YouTube Cache"}},
				},
			})
		} else {
			writeSubsonicOK(w, f, `<musicFolders><musicFolder id="1" name="YouTube Cache"/></musicFolders>`)
		}
	case "getindexes", "getartists":
		mu.RLock()
		count := len(db)
		mu.RUnlock()
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"artists": map[string]interface{}{
					"index": []map[string]interface{}{
						{"name": "Y", "artist": []map[string]interface{}{
							{"id": "ar-1", "name": "YouTube", "albumCount": 1, "coverArt": "ar-1"},
						}},
					},
				},
				"indexes": map[string]interface{}{ // for getIndexes
					"index": []map[string]interface{}{
						{"name": "Y", "artist": []map[string]interface{}{
							{"id": "ar-1", "name": "YouTube"},
						}},
					},
					"lastModified": time.Now().UnixMilli(),
				},
			})
		} else {
			xmlBody := fmt.Sprintf(`<indexes lastModified="%d"><index name="Y"><artist id="ar-1" name="YouTube"/></index></indexes>
			<artists><index name="Y"><artist id="ar-1" name="YouTube" albumCount="%d"/></index></artists>`, time.Now().UnixMilli(), count)
			writeSubsonicOK(w, f, xmlBody)
		}
	case "getartist":
		id := r.URL.Query().Get("id")
		if id != "ar-1" {
			writeSubsonicError(w, 70, "Artist not found", f)
			return
		}
		mu.RLock()
		defer mu.RUnlock()
		if f == "json" {
			songs := []map[string]interface{}{}
			for _, s := range db {
				songs = append(songs, map[string]interface{}{
					"id": s.YTID, "title": s.Title, "album": "Cached Songs", "albumId": "al-1",
					"artist": "YouTube", "artistId": "ar-1", "coverArt": s.YTID,
					"duration": 180, "bitRate": 128, "suffix": "mp3", "contentType": "audio/mpeg",
					"isDir": false, "path": s.YTID + ".mp3",
				})
			}
			writeSubsonicOK(w, f, map[string]interface{}{
				"artist": map[string]interface{}{
					"id": "ar-1", "name": "YouTube", "albumCount": 1,
					"album": []map[string]interface{}{
						{"id": "al-1", "name": "Cached Songs", "artist": "YouTube", "artistId": "ar-1", "songCount": len(db), "coverArt": "al-1"},
					},
				},
			})
		} else {
			writeSubsonicOK(w, f, `<artist id="ar-1" name="YouTube" albumCount="1"><album id="al-1" name="Cached Songs" artist="YouTube" artistId="ar-1" songCount="`+fmt.Sprint(len(db))+`"/></artist>`)
		}
	case "getalbum", "getmusicdirectory":
		id := r.URL.Query().Get("id")
		mu.RLock()
		defer mu.RUnlock()
		if id == "al-1" || id == "ar-1" || id == "1" {
			if f == "json" {
				children := []map[string]interface{}{}
				for _, s := range db {
					children = append(children, map[string]interface{}{
						"id": s.YTID, "title": s.Title, "album": "Cached Songs", "albumId": "al-1",
						"artist": "YouTube", "artistId": "ar-1", "coverArt": s.YTID,
						"duration": 180, "bitRate": 128, "suffix": "mp3", "contentType": "audio/mpeg",
						"isDir": false, "parent": "al-1",
					})
				}
				writeSubsonicOK(w, f, map[string]interface{}{
					"album": map[string]interface{}{
						"id": "al-1", "name": "Cached Songs", "artist": "YouTube", "artistId": "ar-1",
						"songCount": len(db), "coverArt": "al-1", "song": children,
					},
					"directory": map[string]interface{}{
						"id": "al-1", "name": "Cached Songs", "child": children,
					},
				})
			} else {
				var b strings.Builder
				b.WriteString(`<album id="al-1" name="Cached Songs" artist="YouTube" artistId="ar-1" songCount="` + fmt.Sprint(len(db)) + `">`)
				for _, s := range db {
					b.WriteString(fmt.Sprintf(`<song id="%s" title="%s" album="Cached Songs" albumId="al-1" artist="YouTube" artistId="ar-1" coverArt="%s" duration="180" bitRate="128" suffix="mp3" contentType="audio/mpeg" isDir="false"/>`, s.YTID, xmlEscape(s.Title), s.YTID))
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
		song, ok := db[id]
		mu.RUnlock()
		if !ok {
			writeSubsonicError(w, 70, "Song not found", f)
			return
		}
		if f == "json" {
			writeSubsonicOK(w, f, map[string]interface{}{
				"song": map[string]interface{}{
					"id": song.YTID, "title": song.Title, "album": "Cached Songs", "albumId": "al-1",
					"artist": "YouTube", "artistId": "ar-1", "coverArt": song.YTID,
					"duration": 180, "bitRate": 128, "suffix": "mp3", "contentType": "audio/mpeg",
				},
			})
		} else {
			writeSubsonicOK(w, f, fmt.Sprintf(`<song id="%s" title="%s" album="Cached Songs" albumId="al-1" artist="YouTube" artistId="ar-1" coverArt="%s" duration="180" bitRate="128" suffix="mp3" contentType="audio/mpeg"/>`, song.YTID, xmlEscape(song.Title), song.YTID))
		}
	case "stream", "download":
		id := r.URL.Query().Get("id")
		if id == "" {
			writeSubsonicError(w, 10, "Missing id", f)
			return
		}
		// Reuse play logic by setting url
		r.URL.RawQuery = "url=https://www.youtube.com/watch?v=" + id
		playHandler(w, r)
	case "getcoverart":
		id := r.URL.Query().Get("id")
		// Try YouTube thumbnail
		if len(id) == 11 { // typical yt id
			http.Redirect(w, r, "https://i.ytimg.com/vi/"+id+"/hqdefault.jpg", 302)
			return
		}
		// Placeholder 1x1 PNG
		w.Header().Set("Content-Type", "image/png")
		// minimal 1x1 red pixel
		w.Write([]byte{
			0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
			0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
			0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
			0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d, 0xb0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
			0x44, 0xae, 0x42, 0x60, 0x82,
		})
	case "search2", "search3":
		q := strings.ToLower(r.URL.Query().Get("query"))
		mu.RLock()
		defer mu.RUnlock()
		var matched []Song
		for _, s := range db {
			if q == "" || strings.Contains(strings.ToLower(s.Title), q) || strings.Contains(strings.ToLower(s.YTID), q) {
				matched = append(matched, s)
			}
		}
		if f == "json" {
			songs := []map[string]interface{}{}
			for _, s := range matched {
				songs = append(songs, map[string]interface{}{
					"id": s.YTID, "title": s.Title, "album": "Cached Songs", "artist": "YouTube",
					"coverArt": s.YTID, "duration": 180, "suffix": "mp3",
				})
			}
			writeSubsonicOK(w, f, map[string]interface{}{
				"searchResult2": map[string]interface{}{"song": songs},
				"searchResult3": map[string]interface{}{"song": songs},
			})
		} else {
			var b strings.Builder
			b.WriteString(`<searchResult3>`)
			for _, s := range matched {
				b.WriteString(fmt.Sprintf(`<song id="%s" title="%s" album="Cached Songs" artist="YouTube" coverArt="%s" duration="180" suffix="mp3"/>`, s.YTID, xmlEscape(s.Title), s.YTID))
			}
			b.WriteString(`</searchResult3>`)
			writeSubsonicOK(w, f, b.String())
		}
	case "getalbumlist", "getalbumlist2":
		mu.RLock()
		defer mu.RUnlock()
		// newest first
		type kv struct {
			id  string
			s   Song
			t   time.Time
		}
		var list []kv
		for id, s := range db {
			t, _ := time.Parse(time.RFC3339, s.AddedAt)
			list = append(list, kv{id, s, t})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].t.After(list[j].t) })

		size := 20
		if len(list) < size {
			size = len(list)
		}
		list = list[:size]

		if f == "json" {
			albums := []map[string]interface{}{}
			for _, item := range list {
				albums = append(albums, map[string]interface{}{
					"id": "al-" + item.id, "name": item.s.Title, "artist": "YouTube", "artistId": "ar-1",
					"coverArt": item.id, "songCount": 1, "created": item.s.AddedAt,
				})
			}
			writeSubsonicOK(w, f, map[string]interface{}{
				"albumList":  map[string]interface{}{"album": albums},
				"albumList2": map[string]interface{}{"album": albums},
			})
		} else {
			var b strings.Builder
			b.WriteString(`<albumList2>`)
			for _, item := range list {
				b.WriteString(fmt.Sprintf(`<album id="al-%s" name="%s" artist="YouTube" artistId="ar-1" coverArt="%s" songCount="1"/>`, item.id, xmlEscape(item.s.Title), item.id))
			}
			b.WriteString(`</albumList2>`)
			writeSubsonicOK(w, f, b.String())
		}
	case "getrandomsongs":
		mu.RLock()
		defer mu.RUnlock()
		var songs []Song
		for _, s := range db {
			songs = append(songs, s)
		}
		// simple shuffle
		for i := range songs {
			j := int(time.Now().UnixNano()) % len(songs)
			songs[i], songs[j] = songs[j], songs[i]
		}
		size := 20
		if len(songs) < size {
			size = len(songs)
		}
		songs = songs[:size]

		if f == "json" {
			out := []map[string]interface{}{}
			for _, s := range songs {
				out = append(out, map[string]interface{}{
					"id": s.YTID, "title": s.Title, "album": "Cached Songs", "artist": "YouTube",
					"coverArt": s.YTID, "duration": 180, "suffix": "mp3",
				})
			}
			writeSubsonicOK(w, f, map[string]interface{}{"randomSongs": map[string]interface{}{"song": out}})
		} else {
			var b strings.Builder
			b.WriteString(`<randomSongs>`)
			for _, s := range songs {
				b.WriteString(fmt.Sprintf(`<song id="%s" title="%s" album="Cached Songs" artist="YouTube" coverArt="%s" duration="180" suffix="mp3"/>`, s.YTID, xmlEscape(s.Title), s.YTID))
			}
			b.WriteString(`</randomSongs>`)
			writeSubsonicOK(w, f, b.String())
		}
	case "scrobble":
		writeSubsonicOK(w, f, map[string]interface{}{})
	default:
		writeSubsonicError(w, 0, "Not implemented: "+path, f)
	}
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// -------------------- Main --------------------

func main() {
	loadDB()
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

	// Subsonic
	http.HandleFunc("/rest/", subsonicHandler)

	user, _ := getAdminCreds()
	log.Printf("Listening on 0.0.0.0:%s  |  Subsonic ready  |  Admin user: %s", port, user)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}