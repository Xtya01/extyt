
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
	enableCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(200)
		return
	}
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
		sendSubsonic(w, r, map[string]interface{}{"license": map[string]interface{}{"valid": true, "email": "extyt@submuz.local", "licenseExpires": "2029-09-11T19:00:00"}}, `<license valid="true" email="extyt@submuz.local"/>`)
	case "getopensubsonicextensions":
		sendSubsonic(w, r, map[string]interface{}{"openSubsonicExtensions": map[string]interface{}{"extension": []map[string]interface{}{{"name": "transcode", "versions": []int{1}}, {"name": "formPost", "versions": []int{1}}, {"name": "songLyrics", "versions": []int{1}} }}}, `<openSubsonicExtensions><extension name="transcode" versions="1"/><extension name="formPost" versions="1"/><extension name="songLyrics" versions="1"/></openSubsonicExtensions>`)
	case "getmusicfolders":
		sendSubsonic(w, r, map[string]interface{}{"musicFolders": map[string]interface{}{"musicFolder": []map[string]interface{}{{"id": 0, "name": "Music"}, {"id": 1, "name": "Submuz"}}}}, `<musicFolders><musicFolder id="0" name="Music"/><musicFolder id="1" name="Submuz"/></musicFolders>`)
	case "getuser":
		u := os.Getenv("SUBSONIC_USER")
		if u == "" { u = os.Getenv("ADMIN_USER") }
		if u == "" { u = "admin" }
		sendSubsonic(w, r, map[string]interface{}{"user": map[string]interface{}{"username": u, "email": "admin@submuz.local", "scrobblingEnabled": false, "adminRole": true, "settingsRole": true, "downloadRole": true, "uploadRole": true, "playlistRole": true, "coverArtRole": true, "commentRole": false, "podcastRole": false, "streamRole": true, "jukeboxRole": false, "shareRole": true, "videoConversionRole": false, "avatarLastChanged": "2024-01-01T00:00:00.000Z", "folder": []int{0,1}}}, fmt.Sprintf(`<user username="%s" adminRole="true" scrobblingEnabled="false"><folder>0</folder><folder>1</folder></user>`, xmlEscape(u)))
	case "getusers":
		u := os.Getenv("SUBSONIC_USER")
		if u == "" { u = "admin" }
		sendSubsonic(w, r, map[string]interface{}{"users": map[string]interface{}{"user": []map[string]interface{}{{"username": u, "adminRole": true}}}}, fmt.Sprintf(`<users><user username="%s" adminRole="true"/></users>`, xmlEscape(u)))
	case "getindexes", "getartists":
		// Group by first letter
		sendSubsonic(w, r, map[string]interface{}{
			"artists": map[string]interface{}{"index": []map[string]interface{}{{"name": "Y", "artist": []map[string]interface{}{{"id": "1", "name": "YouTube", "albumCount": 1, "coverArt": "1"}}}}},
			"indexes": map[string]interface{}{"index": []map[string]interface{}{{"name": "Y", "artist": []map[string]interface{}{{"id": "1", "name": "YouTube", "albumCount": 1}}}}, "lastModified": time.Now().UnixMilli()},
		}, `<indexes lastModified="0"><index name="Y"><artist id="1" name="YouTube" albumCount="1"/></index></indexes>`)
	case "getartist":
		sendSubsonic(w, r, map[string]interface{}{"artist": map[string]interface{}{"id": "1", "name": "YouTube", "albumCount": 1, "coverArt": "1", "album": []map[string]interface{}{{"id": "1", "name": "Submuz", "artist": "YouTube", "songCount": len(songs), "coverArt": "1", "created": time.Now().Format(time.RFC3339)}}}}, fmt.Sprintf(`<artist id="1" name="YouTube" albumCount="1"><album id="1" name="Submuz" songCount="%d"/></artist>`, len(songs)))
	case "getartistinfo", "getartistinfo2":
		// Amcfy expects biography, similar artists etc - return empty but valid
		sendSubsonic(w, r, map[string]interface{}{"artistInfo": map[string]interface{}{"biography": "YouTube music collection", "musicBrainzId": "", "lastFmUrl": "", "smallImageUrl": "", "mediumImageUrl": "", "largeImageUrl": ""}, "artistInfo2": map[string]interface{}{"biography": "YouTube music collection", "smallImageUrl": "", "mediumImageUrl": "", "largeImageUrl": ""}}, `<artistInfo><biography>YouTube collection</biography></artistInfo>`)
	case "getalbum":
		var sx strings.Builder
		var sj []map[string]interface{}
		for i, s := range songs {
			j, x := songToSong(s, i)
			sj = append(sj, j)
			sx.WriteString(x)
		}
		sendSubsonic(w, r, map[string]interface{}{"album": map[string]interface{}{"id": "1", "name": "Submuz", "artist": "YouTube", "artistId": "1", "coverArt": "1", "songCount": len(songs), "duration": len(songs)*210, "created": time.Now().Format(time.RFC3339), "year": 2025, "genre": "YouTube", "song": sj}}, fmt.Sprintf(`<album id="1" name="Submuz" artist="YouTube" songCount="%d" duration="%d">%s</album>`, len(songs), len(songs)*210, sx.String()))
	case "getalbuminfo", "getalbuminfo2":
		sendSubsonic(w, r, map[string]interface{}{"albumInfo": map[string]interface{}{"notes": "Submuz collection", "musicBrainzId": "", "lastFmUrl": "", "smallImageUrl": "", "mediumImageUrl": "", "largeImageUrl": ""}, "albumInfo2": map[string]interface{}{"notes": "Submuz collection"}}, `<albumInfo><notes>Submuz collection</notes></albumInfo>`)
	case "getmusicdirectory":
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
		sendSubsonic(w, r, map[string]interface{}{"directory": map[string]interface{}{"id": "1", "name": "Submuz", "childCount": len(songs), "child": jsonChildren}}, fmt.Sprintf(`<directory id="1" name="Submuz" childCount="%d">%s</directory>`, len(songs), xmlChildren.String()))
	case "getgenres":
		sendSubsonic(w, r, map[string]interface{}{"genres": map[string]interface{}{"genre": []map[string]interface{}{{"value": "YouTube", "songCount": len(songs), "albumCount": 1}, {"value": "Music", "songCount": len(songs), "albumCount": 1}}}}, fmt.Sprintf(`<genres><genre songCount="%d" albumCount="1">YouTube</genre></genres>`, len(songs)))
	case "getsong":
		id := r.URL.Query().Get("id")
		cleanID := id
		if strings.Contains(id, "-") {
			parts := strings.Split(id, "-")
			cleanID = parts[len(parts)-1]
		}
		var found *Song
		for _, s := range songs {
			if s.YTID == cleanID || s.YTID == id {
				tmp := s
				found = &tmp
				break
			}
		}
		if found == nil && len(songs) > 0 {
			tmp := songs[0]
			found = &tmp
		}
		if found != nil {
			j, x := songToSong(*found, 0)
			sendSubsonic(w, r, map[string]interface{}{"song": j}, x)
		} else {
			sendSubsonic(w, r, map[string]interface{}{}, "")
		}
	case "getvideos":
		sendSubsonic(w, r, map[string]interface{}{"videos": map[string]interface{}{"video": []interface{}{}}}, `<videos/>`)
	case "getalbumlist", "getalbumlist2":
		// Support types: random, recent, frequent, starred, alphabeticalByName, etc
		typ := r.URL.Query().Get("type")
		if typ == "" { typ = "random" }
		albumJSON := map[string]interface{}{"id": "1", "title": "Submuz", "name": "Submuz", "artist": "YouTube", "artistId": "1", "songCount": len(songs), "coverArt": "1", "created": time.Now().Format(time.RFC3339), "duration": len(songs)*210}
		sendSubsonic(w, r, map[string]interface{}{"albumList": map[string]interface{}{"album": []map[string]interface{}{albumJSON}}, "albumList2": map[string]interface{}{"album": []map[string]interface{}{albumJSON}}}, fmt.Sprintf(`<albumList><album id="1" title="Submuz" songCount="%d"/></albumList>`, len(songs)))
	case "getrandomsongs":
		rand.Shuffle(len(songs), func(i, j int) { songs[i], songs[j] = songs[j], songs[i] })
		limit := 50
		if len(songs) < limit { limit = len(songs) }
		var xb strings.Builder
		var ja []map[string]interface{}
		for i := 0; i < limit; i++ {
			j, x := songToSong(songs[i], i)
			ja = append(ja, j)
			xb.WriteString(x)
		}
		sendSubsonic(w, r, map[string]interface{}{"randomSongs": map[string]interface{}{"song": ja}}, fmt.Sprintf(`<randomSongs>%s</randomSongs>`, xb.String()))
	case "getsongsbygenre":
		genre := r.URL.Query().Get("genre")
		_ = genre
		limit := 50
		if len(songs) < limit { limit = len(songs) }
		var xb strings.Builder
		var ja []map[string]interface{}
		for i := 0; i < limit; i++ {
			j, x := songToSong(songs[i], i)
			ja = append(ja, j)
			xb.WriteString(x)
		}
		sendSubsonic(w, r, map[string]interface{}{"songsByGenre": map[string]interface{}{"song": ja}}, fmt.Sprintf(`<songsByGenre>%s</songsByGenre>`, xb.String()))
	case "getstarred", "getstarred2":
		// Return all as starred for Amcfy favorites
		var xb strings.Builder
		var ja []map[string]interface{}
		for i, s := range songs {
			j, x := songToSong(s, i)
			ja = append(ja, j)
			xb.WriteString(x)
		}
		sendSubsonic(w, r, map[string]interface{}{"starred": map[string]interface{}{"song": ja, "album": []map[string]interface{}{{"id": "1", "name": "Submuz"}}, "artist": []map[string]interface{}{{"id": "1", "name": "YouTube"}}}, "starred2": map[string]interface{}{"song": ja}}, fmt.Sprintf(`<starred>%s</starred>`, xb.String()))
	case "star", "unstar":
		// No-op but success
		sendSubsonic(w, r, map[string]interface{}{}, "")
	case "getsimilarsongs", "getsimilarsongs2", "gettopsongs":
		// Return random songs as similar/top
		rand.Shuffle(len(songs), func(i, j int) { songs[i], songs[j] = songs[j], songs[i] })
		limit := 20
		if len(songs) < limit { limit = len(songs) }
		var xb strings.Builder
		var ja []map[string]interface{}
		for i := 0; i < limit; i++ {
			j, x := songToSong(songs[i], i)
			ja = append(ja, j)
			xb.WriteString(x)
		}
		sendSubsonic(w, r, map[string]interface{}{"similarSongs": map[string]interface{}{"song": ja}, "similarSongs2": map[string]interface{}{"song": ja}, "topSongs": map[string]interface{}{"song": ja}}, fmt.Sprintf(`<similarSongs>%s</similarSongs>`, xb.String()))
	case "search", "search2", "search3":
		q := strings.ToLower(r.URL.Query().Get("query"))
		if q == "" { q = strings.ToLower(r.URL.Query().Get("q")) }
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
		sendSubsonic(w, r, map[string]interface{}{
			"searchResult": map[string]interface{}{"match": ja},
			"searchResult2": map[string]interface{}{"song": ja},
			"searchResult3": map[string]interface{}{"song": ja, "album": []map[string]interface{}{{"id": "1", "name": "Submuz"}}, "artist": []map[string]interface{}{{"id": "1", "name": "YouTube"}}},
		}, fmt.Sprintf(`<searchResult3><song>%s</song></searchResult3>`, xb.String()))
	case "getplaylists":
		sendSubsonic(w, r, map[string]interface{}{"playlists": map[string]interface{}{"playlist": []map[string]interface{}{{"id": "1", "name": "Submuz", "songCount": len(songs), "duration": len(songs)*210, "public": true, "owner": "admin", "created": time.Now().Format(time.RFC3339)}}}}, fmt.Sprintf(`<playlists><playlist id="1" name="Submuz" songCount="%d" duration="%d"/></playlists>`, len(songs), len(songs)*210))
	case "getplaylist":
		id := r.URL.Query().Get("id")
		if id == "" { id = "1" }
		var xb strings.Builder
		var ja []map[string]interface{}
		for i, s := range songs {
			j, x := songToSong(s, i)
			ja = append(ja, j)
			xb.WriteString(x)
		}
		sendSubsonic(w, r, map[string]interface{}{"playlist": map[string]interface{}{"id": id, "name": "Submuz", "songCount": len(songs), "duration": len(songs)*210, "entry": ja}}, fmt.Sprintf(`<playlist id="%s" name="Submuz" songCount="%d">%s</playlist>`, id, len(songs), xb.String()))
	case "createplaylist", "updateplaylist", "deleteplaylist":
		sendSubsonic(w, r, map[string]interface{}{"playlist": map[string]interface{}{"id": "1", "name": "Submuz"}}, `<playlist id="1" name="Submuz"/>`)
	case "getcoverart":
		id := r.URL.Query().Get("id")
		if id == "" { id = "dQw4w9WgXcQ" }
		cleanID := id
		if strings.Contains(id, "-") {
			parts := strings.Split(id, "-")
			cleanID = parts[len(parts)-1]
		}
		thumb := fmt.Sprintf("https://img.youtube.com/vi/%s/mqdefault.jpg", cleanID)
		http.Redirect(w, r, thumb, 302)
	case "getavatar":
		w.Header().Set("Content-Type", "image/jpeg")
		// Return 1x1 transparent or redirect to default avatar
		http.Redirect(w, r, "https://img.youtube.com/vi/dQw4w9WgXcQ/mqdefault.jpg", 302)
	case "getlyrics", "getlyricsbysongid":
		// Amcfy expects lyrics
		sendSubsonic(w, r, map[string]interface{}{"lyrics": map[string]interface{}{"artist": "YouTube", "title": "Lyrics", "value": ""}}, `<lyrics artist="YouTube" title="Lyrics"></lyrics>`)
	case "scrobble":
		sendSubsonic(w, r, map[string]interface{}{}, "")
	case "getscanstatus", "startscan":
		sendSubsonic(w, r, map[string]interface{}{"scanStatus": map[string]interface{}{"scanning": false, "count": len(songs)}}, fmt.Sprintf(`<scanStatus scanning="false" count="%d"/>`, len(songs)))
	case "stream", "download":
		id := r.URL.Query().Get("id")
		cleanID := id
		if strings.Contains(id, "-") {
			parts := strings.Split(id, "-")
			cleanID = parts[len(parts)-1]
		}
		mu.RLock()
		song, ok := db[cleanID]
		if !ok { song, ok = db[id] }
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
		if fp == "" { fp = getTelegramFilePath(song.FileID) }
		if token != "" && fp != "" {
			direct := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, fp)
			http.Redirect(w, r, direct, 302)
			return
		}
		http.Error(w, "file not found", 404)
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
	http.HandleFunc("/rest/", subsonicHandler)
	log.Println("Listening on 0.0.0.0:" + port + " with Subsonic API v10")
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}
