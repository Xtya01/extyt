
package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	writer.WriteField("caption", fmt.Sprintf("DB backup %s - %d songs - file_id ko ENV DB_JSON_FILE_ID me daalo", time.Now().Format("2006-01-02 15:04"), len(db)))
	writer.Close()
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", token)
	req, _ := http.NewRequest("POST", url, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		log.Println("DB backup:", string(b))
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

func getCookiesArg() []string {
	if b64 := os.Getenv("YT_COOKIES_B64"); b64 != "" {
		b64 = strings.TrimSpace(b64)
		b64 = strings.ReplaceAll(b64, "\n", "")
		b64 = strings.ReplaceAll(b64, "\r", "")
		b64 = strings.ReplaceAll(b64, " ", "")
		if decoded, err := base64.StdEncoding.DecodeString(b64); err == nil {
			os.WriteFile(cookiePath, decoded, 0644)
			return []string{"--cookies", cookiePath}
		}
		if decoded, err := base64.RawStdEncoding.DecodeString(b64); err == nil {
			os.WriteFile(cookiePath, decoded, 0644)
			return []string{"--cookies", cookiePath}
		}
	}
	if raw := os.Getenv("YT_COOKIES"); raw != "" {
		os.WriteFile(cookiePath, []byte(raw), 0644)
		return []string{"--cookies", cookiePath}
	}
	candidates := []string{"/app/cookies.txt", "./cookies.txt", "cookies.txt", "/tmp/cookies.txt", cookiePath}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && info.Size() > 100 {
			return []string{"--cookies", p}
		}
	}
	return []string{}
}

func isSubsonicAuthOK(r *http.Request) bool {
	user := os.Getenv("SUBSONIC_USER")
	pass := os.Getenv("SUBSONIC_PASS")
	if user == "" && pass == "" {
		return true
	}
	q := r.URL.Query()
	u := q.Get("u")
	p := q.Get("p")
	t := q.Get("t")
	s := q.Get("s")
	if user != "" && u != user {
		return false
	}
	if p != "" && p == pass {
		return true
	}
	if t != "" && s != "" {
		h := md5.Sum([]byte(pass + s))
		if fmt.Sprintf("%x", h) == t {
			return true
		}
	}
	if pass == "" {
		return true
	}
	return false
}

func subsonicBase() map[string]interface{} {
	return map[string]interface{}{
		"status": "ok", "version": "1.16.1", "type": "extyt", "serverVersion": "v10-m4a-subsonic", "openSubsonic": true,
	}
}

func sendSubsonic(w http.ResponseWriter, r *http.Request, jsonExtra map[string]interface{}, xmlInner string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	if !isSubsonicAuthOK(r) {
		if r.URL.Query().Get("f") == "json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"subsonic-response": map[string]interface{}{"status": "failed", "version": "1.16.1", "error": map[string]interface{}{"code": 40, "message": "Wrong username or password"}}})
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><subsonic-response xmlns="http://subsonic.org/restapi" status="failed" version="1.16.1"><error code="40" message="Wrong username or password"/></subsonic-response>`)
		return
	}
	if r.URL.Query().Get("f") == "json" {
		base := subsonicBase()
		for k, v := range jsonExtra {
			base[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"subsonic-response": base})
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1" type="extyt" serverVersion="v10-m4a-subsonic" openSubsonic="true">%s</subsonic-response>`, xmlInner)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", """, "&quot;", "'", "&apos;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func songToChild(s Song, idx int) (map[string]interface{}, string) {
	title := s.Title
	if title == "" {
		title = s.YTID
	}
	created := s.AddedAt
	if created == "" {
		created = time.Now().Format(time.RFC3339)
	}
	j := map[string]interface{}{
		"id": "1-" + s.YTID, "parent": "1", "isDir": false, "title": title, "album": "Submuz", "artist": "YouTube",
		"track": idx + 1, "year": 2025, "genre": "YouTube", "coverArt": s.YTID, "size": 3500000,
		"contentType": "audio/x-m4a", "suffix": "m4a", "duration": 210, "bitRate": 128,
		"path": fmt.Sprintf("Submuz/%s.m4a", s.YTID), "playCount": 1, "created": created,
		"albumId": "1", "artistId": "1", "type": "music",
	}
	x := fmt.Sprintf(`<child id="%s" parent="1" isDir="false" title="%s" album="Submuz" artist="YouTube" track="%d" coverArt="%s" suffix="m4a" contentType="audio/x-m4a" duration="210"/>`, "1-"+s.YTID, xmlEscape(title), idx+1, s.YTID)
	return j, x
}

func songToSong(s Song, idx int) (map[string]interface{}, string) {
	title := s.Title
	if title == "" {
		title = s.YTID
	}
	created := s.AddedAt
	if created == "" {
		created = time.Now().Format(time.RFC3339)
	}
	j := map[string]interface{}{
		"id": s.YTID, "parent": "1", "isDir": false, "title": title, "album": "Submuz", "artist": "YouTube",
		"track": idx + 1, "year": 2025, "genre": "YouTube", "coverArt": s.YTID, "size": 3500000,
		"contentType": "audio/x-m4a", "suffix": "m4a", "duration": 210, "bitRate": 128,
		"path": fmt.Sprintf("Submuz/%s.m4a", s.YTID), "playCount": 1, "created": created,
		"albumId": "1", "artistId": "1", "type": "music",
	}
	x := fmt.Sprintf(`<song id="%s" parent="1" isDir="false" title="%s" album="Submuz" artist="YouTube" track="%d" coverArt="%s" suffix="m4a" contentType="audio/x-m4a" duration="210"/>`, s.YTID, xmlEscape(title), idx+1, s.YTID)
	return j, x
}

func subsonicHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/rest/")
	path = strings.TrimSuffix(path, ".view")
	endpoint := strings.ToLower(strings.Split(path, "/")[0])

	mu.RLock()
	songs := make([]Song, 0, len(db))
	for _, v := range db {
		songs = append(songs, v)
	}
	mu.RUnlock()

	switch endpoint {
	case "ping":
		sendSubsonic(w, r, map[string]interface{}{}, "")
	case "getlicense":
		sendSubsonic(w, r, map[string]interface{}{"license": map[string]interface{}{"valid": true, "email": "extyt@submuz.local"}}, `<license valid="true" email="extyt@submuz.local"/>`)
	case "getmusicfolders":
		sendSubsonic(w, r, map[string]interface{}{"musicFolders": map[string]interface{}{"musicFolder": []map[string]interface{}{{"id": 0, "name": "Music"}, {"id": 1, "name": "Submuz"}}}}, `<musicFolders><musicFolder id="0" name="Music"/><musicFolder id="1" name="Submuz"/></musicFolders>`)
	case "getindexes", "getartists":
		sendSubsonic(w, r, map[string]interface{}{
			"artists": map[string]interface{}{"index": []map[string]interface{}{{"name": "Y", "artist": []map[string]interface{}{{"id": "1", "name": "YouTube", "albumCount": 1}}}}},
			"indexes": map[string]interface{}{"index": []map[string]interface{}{{"name": "Y", "artist": []map[string]interface{}{{"id": "1", "name": "YouTube"}}}}, "lastModified": 0},
		}, `<indexes><index name="Y"><artist id="1" name="YouTube"/></index></indexes>`)
	case "getartist":
		sendSubsonic(w, r, map[string]interface{}{"artist": map[string]interface{}{"id": "1", "name": "YouTube", "albumCount": 1, "album": []map[string]interface{}{{"id": "1", "name": "Submuz", "artist": "YouTube", "songCount": len(songs)}}}}, fmt.Sprintf(`<artist id="1" name="YouTube"><album id="1" name="Submuz" songCount="%d"/></artist>`, len(songs)))
	case "getalbum", "getmusicdirectory":
		var xmlChildren strings.Builder
		var jsonChildren []map[string]interface{}
		for i, s := range songs {
			j, _ := songToChild(s, i)
			jsonChildren = append(jsonChildren, j)
		}
		for i, s := range songs {
			_, x := songToChild(s, i)
			xmlChildren.WriteString(x)
		}
		if endpoint == "getmusicdirectory" {
			sendSubsonic(w, r, map[string]interface{}{"directory": map[string]interface{}{"id": "1", "name": "Submuz", "childCount": len(songs), "child": jsonChildren}}, fmt.Sprintf(`<directory id="1" name="Submuz" childCount="%d">%s</directory>`, len(songs), xmlChildren.String()))
		} else {
			var sx strings.Builder
			var sj []map[string]interface{}
			for i, s := range songs {
				j, x := songToSong(s, i)
				sj = append(sj, j)
				sx.WriteString(x)
			}
			sendSubsonic(w, r, map[string]interface{}{"album": map[string]interface{}{"id": "1", "name": "Submuz", "artist": "YouTube", "songCount": len(songs), "song": sj}}, fmt.Sprintf(`<album id="1" name="Submuz" songCount="%d">%s</album>`, len(songs), sx.String()))
		}
	case "getalbumlist", "getalbumlist2":
		albumJSON := map[string]interface{}{"id": "1", "title": "Submuz", "name": "Submuz", "artist": "YouTube", "songCount": len(songs), "coverArt": "1"}
		sendSubsonic(w, r, map[string]interface{}{"albumList": map[string]interface{}{"album": []map[string]interface{}{albumJSON}}, "albumList2": map[string]interface{}{"album": []map[string]interface{}{albumJSON}}}, fmt.Sprintf(`<albumList><album id="1" title="Submuz" songCount="%d"/></albumList>`, len(songs)))
	case "getrandomsongs", "getstarred", "getstarred2", "getsongsbygenre":
		rand.Shuffle(len(songs), func(i, j int) { songs[i], songs[j] = songs[j], songs[i] })
		limit := 50
		if len(songs) < limit {
			limit = len(songs)
		}
		var xb strings.Builder
		var ja []map[string]interface{}
		for i := 0; i < limit; i++ {
			j, x := songToSong(songs[i], i)
			ja = append(ja, j)
			xb.WriteString(x)
		}
		sendSubsonic(w, r, map[string]interface{}{"randomSongs": map[string]interface{}{"song": ja}, "starred": map[string]interface{}{"song": ja}}, fmt.Sprintf(`<randomSongs>%s</randomSongs>`, xb.String()))
	case "search", "search2", "search3":
		q := strings.ToLower(r.URL.Query().Get("query"))
		if q == "" {
			q = strings.ToLower(r.URL.Query().Get("q"))
		}
		var matched []Song
		for _, s := range songs {
			if q == "" || strings.Contains(strings.ToLower(s.Title), q) || strings.Contains(strings.ToLower(s.YTID), q) {
				matched = append(matched, s)
			}
		}
		var xb strings.Builder
		var ja []map[string]interface{}
		for i, s := range matched {
			j, x := songToSong(s, i)
			ja = append(ja, j)
			xb.WriteString(x)
		}
		sendSubsonic(w, r, map[string]interface{}{"searchResult3": map[string]interface{}{"song": ja}}, fmt.Sprintf(`<searchResult3>%s</searchResult3>`, xb.String()))
	case "stream", "download":
		id := r.URL.Query().Get("id")
		cleanID := id
		if strings.Contains(id, "-") {
			parts := strings.Split(id, "-")
			cleanID = parts[len(parts)-1]
		}
		mu.RLock()
		song, ok := db[cleanID]
		if !ok {
			song, ok = db[id]
		}
		mu.RUnlock()
		if !ok {
			for _, s := range songs {
				if s.YTID == cleanID {
					song = s
					ok = true
					break
				}
			}
		}
		if !ok {
			http.Error(w, "not found", 404)
			return
		}
		token := os.Getenv("BOT_TOKEN")
		fp := song.FilePath
		if fp == "" {
			fp = getTelegramFilePath(song.FileID)
		}
		if token != "" && fp != "" {
			direct := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, fp)
			http.Redirect(w, r, direct, 302)
			return
		}
		http.Error(w, "file not found", 404)
	case "getcoverart":
		id := r.URL.Query().Get("id")
		if id == "" {
			id = "dQw4w9WgXcQ"
		}
		cleanID := id
		if strings.Contains(id, "-") {
			cleanID = strings.Split(id, "-")[1]
			if len(strings.Split(id, "-")) > 1 {
				cleanID = strings.Split(id, "-")[len(strings.Split(id, "-"))-1]
			}
		}
		thumb := fmt.Sprintf("https://img.youtube.com/vi/%s/mqdefault.jpg", cleanID)
		http.Redirect(w, r, thumb, 302)
	case "getopensubsonicextensions":
		sendSubsonic(w, r, map[string]interface{}{"openSubsonicExtensions": map[string]interface{}{"extension": []map[string]interface{}{{"name": "transcode", "version": 1}}}}, `<openSubsonicExtensions><extension name="transcode" version="1"/></openSubsonicExtensions>`)
	default:
		sendSubsonic(w, r, map[string]interface{}{}, "")
	}
}

func health(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	c := len(db)
	mu.RUnlock()
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(fmt.Sprintf("OK v10-m4a-subsonic - %d songs - Subsonic: /rest/ping", c)))
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

	mu.RLock()
	if song, ok := db[ytID]; ok && song.FileID != "" {
		mu.RUnlock()
		token := os.Getenv("BOT_TOKEN")
		fp := song.FilePath
		if fp == "" {
			fp = getTelegramFilePath(song.FileID)
		}
		if token != "" && fp != "" {
			direct := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, fp)
			http.Redirect(w, r, direct, 302)
			return
		}
	}
	mu.RUnlock()

	sem <- struct{}{}
	defer func() { <-sem }()

	tmpFile := filepath.Join("/tmp", ytID+".m4a")
	os.Remove(tmpFile)

	cookieArgs := getCookiesArg()
	ytArgs := []string{"-x", "--audio-format", "m4a", "--audio-quality", "0", "--no-playlist", "--no-check-certificate", "--js-runtimes", "node:deno", "--extractor-args", "youtube:player_client=web", "-o", tmpFile}
	ytArgs = append(cookieArgs, ytArgs...)
	ytArgs = append(ytArgs, ytUrl)

	cmd := exec.Command("yt-dlp", ytArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("yt-dlp failed: %v\n%s", err, string(out)), 500)
		return
	}

	title := ytID
	cmd2 := exec.Command("yt-dlp", append(cookieArgs, "--skip-download", "--print", "%(title)s", ytUrl)...)
	if out2, err := cmd2.Output(); err == nil {
		t := strings.TrimSpace(string(out2))
		if t != "" {
			title = t
		}
	}

	token := os.Getenv("BOT_TOKEN")
	chatID := os.Getenv("CHANNEL_ID")
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
			url := fmt.Sprintf("https://api.telegram.org/bot%s/sendAudio", token)
			req, _ := http.NewRequest("POST", url, body)
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

	w.Header().Set("Content-Type", "audio/mp4")
	http.ServeFile(w, r, tmpFile)
}


func adminLoginHandler(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    if r.Method == "OPTIONS" { w.WriteHeader(200); return }
    if r.Method != "POST" {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "POST only"})
        return
    }
    var req struct {
        User string `json:"user"`
        Pass string `json:"pass"`
        Username string `json:"username"`
        Password string `json:"password"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "invalid json", 400)
        return
    }
    u := req.User
    if u == "" { u = req.Username }
    pw := req.Pass
    if pw == "" { pw = req.Password }

    expUser := os.Getenv("ADMIN_USER")
    if expUser == "" { expUser = "admin" }
    expPass := os.Getenv("ADMIN_PASS")
    if expPass == "" { expPass = "admin123" }

    w.Header().Set("Content-Type", "application/json")
    if u == expUser && pw == expPass {
        // simple token = base64 user:pass
        token := base64.StdEncoding.EncodeToString([]byte(u + ":" + pw))
        json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "token": token, "user": u})
    } else {
        w.WriteHeader(401)
        json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "wrong credentials"})
    }
}

func adminCheckHandler(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    if r.Method == "OPTIONS" { w.WriteHeader(200); return }
    auth := r.Header.Get("Authorization")
    if auth == "" { auth = r.URL.Query().Get("token") }
    if strings.HasPrefix(auth, "Bearer ") { auth = strings.TrimPrefix(auth, "Bearer ") }
    
    expUser := os.Getenv("ADMIN_USER")
    if expUser == "" { expUser = "admin" }
    expPass := os.Getenv("ADMIN_PASS")
    if expPass == "" { expPass = "admin123" }
    expectedToken := base64.StdEncoding.EncodeToString([]byte(expUser + ":" + expPass))
    
    w.Header().Set("Content-Type", "application/json")
    if auth == expectedToken || auth == expUser+":"+expPass {
        json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
    } else {
        w.WriteHeader(401)
        json.NewEncoder(w).Encode(map[string]interface{}{"ok": false})
    }
}

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
	http.HandleFunc("/admin/login", adminLoginHandler)
	http.HandleFunc("/admin/check", adminCheckHandler)
	http.HandleFunc("/convert", playHandler)
	http.HandleFunc("/rest/", subsonicHandler)
	log.Println("Listening on 0.0.0.0:" + port + " with Subsonic API v10")
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}
