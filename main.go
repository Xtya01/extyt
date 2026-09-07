package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	sem = make(chan struct{}, 1) // 512 MB Nano = 1 job only
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

// ---------- COOKIES FIX - YT bot check ke liye ----------
func getCookiesArg() []string {
	if b64 := os.Getenv("YT_COOKIES_B64"); b64 != "" {
		b64 = strings.TrimSpace(b64)
		b64 = strings.ReplaceAll(b64, "\n", "")
		b64 = strings.ReplaceAll(b64, "\r", "")
		b64 = strings.ReplaceAll(b64, " ", "")
		if decoded, err := base64.StdEncoding.DecodeString(b64); err == nil {
			os.WriteFile(cookiePath, decoded, 0644)
			log.Println("Using cookies from YT_COOKIES_B64 ENV, size:", len(decoded))
			return []string{"--cookies", cookiePath}
		} else {
			if decoded, err := base64.RawStdEncoding.DecodeString(b64); err == nil {
				os.WriteFile(cookiePath, decoded, 0644)
				log.Println("Using cookies from YT_COOKIES_B64 (raw), size:", len(decoded))
				return []string{"--cookies", cookiePath}
			}
			log.Println("Failed to decode YT_COOKIES_B64:", err)
		}
	}
	if raw := os.Getenv("YT_COOKIES"); raw != "" {
		os.WriteFile(cookiePath, []byte(raw), 0644)
		log.Println("Using cookies from YT_COOKIES ENV")
		return []string{"--cookies", cookiePath}
	}
	candidates := []string{"/app/cookies.txt", "./cookies.txt", "cookies.txt", "/tmp/cookies.txt", cookiePath}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && info.Size() > 100 {
			log.Println("Using cookies file:", p, "size", info.Size())
			return []string{"--cookies", p}
		}
	}
	log.Println("WARNING: No cookies found! Bot check ayega")
	return []string{}
}

func health(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	c := len(db)
	mu.RUnlock()
	cookies := getCookiesArg()
	hasCookies := len(cookies) > 0
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(fmt.Sprintf("OK v8 - %d songs - cookies: %v", c, hasCookies)))
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

	tmpFile := filepath.Join("/tmp", ytID+".mp3")
	os.Remove(tmpFile)

	cookieArgs := getCookiesArg()
	// FIXED LINE - ye thi galti:
	// Purana: "--extractor-args", "youtube:player_client=android,web", "--user-agent", "Mozilla/5.0 (Linux; Android 10)..."
	// Naya: android hata diya, js-runtimes add kiya
	ytArgs := []string{"-x", "--audio-format", "mp3", "--no-playlist", "--no-check-certificate", "--js-runtimes", "node:deno", "--remote-components", "ejs:github", "--extractor-args", "youtube:player_client=web", "-o", tmpFile}
	ytArgs = append(cookieArgs, ytArgs...)
	ytArgs = append(ytArgs, ytUrl)

	log.Println("Running yt-dlp with cookies:", len(cookieArgs) > 0)
	cmd := exec.Command("yt-dlp", ytArgs...)
	out, err := cmd.CombinedOutput()
	log.Printf("yt-dlp out: %s", string(out))
	if err != nil {
		http.Error(w, fmt.Sprintf("yt-dlp failed: %v\nLog:\n%s\n\nCheck: YT_COOKIES_B64 ENV sahi hai? File size > 1000 hona chahiye. Health pe cookies: true dikhna chahiye", err, string(out)), 500)
		return
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
					db[ytID] = Song{YTID: ytID, Title: ytID, FileID: res.Result.Audio.FileID, FilePath: fp, AddedAt: time.Now().Format(time.RFC3339)}
					mu.Unlock()
					saveDB()
				}
			}
		}
	}

	w.Header().Set("Content-Type", "audio/mpeg")
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
	log.Println("Listening on 0.0.0.0:" + port)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}
