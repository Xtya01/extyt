package main

import (
	"bytes"
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
	YTID      string `json:"yt_id"`
	Title     string `json:"title"`
	FileID    string `json:"file_id"`    // Telegram file_id of audio
	FilePath  string `json:"file_path"`  // Telegram file_path for direct download
	Duration  int    `json:"duration"`
	AddedAt   string `json:"added_at"`
}

var (
	db   = make(map[string]Song) // key = ytID
	mu   sync.RWMutex
	sem  = make(chan struct{}, 1) // 512 MB pe 1 job only
)

const dbPath = "/tmp/db.json"

// ---------- JSON DB with Telegram backup ----------
func loadDB() {
	mu.Lock()
	defer mu.Unlock()

	// 1. Local se try karo
	if data, err := os.ReadFile(dbPath); err == nil {
		json.Unmarshal(data, &db)
		log.Printf("DB loaded local: %d songs", len(db))
		return
	}

	// 2. ENV se Telegram file_id se try karo (persistent restore)
	if fileID := os.Getenv("DB_JSON_FILE_ID"); fileID != "" {
		if err := downloadDBFromTelegram(fileID); err == nil {
			if data, err := os.ReadFile(dbPath); err == nil {
				json.Unmarshal(data, &db)
				log.Printf("DB restored from Telegram: %d songs", len(db))
				return
			}
		}
	}

	// 3. Naya banao
	db = make(map[string]Song)
	log.Println("New empty DB created")
}

func saveDB() {
	mu.RLock()
	data, _ := json.MarshalIndent(db, "", "  ")
	mu.RUnlock()

	os.WriteFile(dbPath, data, 0644)
	log.Printf("DB saved local: %d songs", len(db))

	// Telegram pe backup async
	go backupDBToTelegram()
}

func backupDBToTelegram() {
	token := os.Getenv("BOT_TOKEN")
	chatID := os.Getenv("CHANNEL_ID")
	if token == "" || chatID == "" {
		return
	}
	// sendDocument API se db.json upload
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
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		log.Println("DB backup to Telegram:", string(b[:200]))
		// Is response me naya file_id milega, usko log me dekho aur ENV me update kar sakte ho future restore ke liye
	}
}

func downloadDBFromTelegram(fileID string) error {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return fmt.Errorf("no token")
	}
	// getFile -> file_path
	getFileURL := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", token, fileID)
	resp, err := http.Get(getFileURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var gf struct {
		Ok     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&gf)
	if !gf.Ok {
		return fmt.Errorf("getFile failed")
	}
	// download file
	dlURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, gf.Result.FilePath)
	r2, err := http.Get(dlURL)
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
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", token, fileID)
	resp, err := http.Get(url)
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
	json.NewDecoder(resp.Body).Decode(&res)
	if res.Ok {
		return res.Result.FilePath
	}
	return ""
}

func getCookiesArg() []string {
	if b64 := os.Getenv("YT_COOKIES_B64"); b64 != "" {
		// base64 decode handled in bash? yaha simple file se
		// For brevity, assume cookies.txt already at /tmp
	}
	candidates := []string{"/app/cookies.txt", "./cookies.txt", "cookies.txt", "/tmp/cookies.txt"}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return []string{"--cookies", p}
		}
	}
	return []string{}
}

// ---------- Handlers ----------
func health(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	count := len(db)
	mu.RUnlock()
	w.Write([]byte(fmt.Sprintf("OK - v6 telegram-json - %d songs indexed", count)))
}

// /list -> JSON index dikhao
func listHandler(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(db)
}

// /db -> db.json download karo
func dbHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, dbPath)
}

// /play?url=YT_URL -> Telegram cache ya YT se stream + Telegram pe save
func playHandler(w http.ResponseWriter, r *http.Request) {
	ytUrl := r.URL.Query().Get("url")
	if ytUrl == "" {
		http.Error(w, "use /play?url=YT_URL", 400)
		return
	}
	// YT ID nikalo simple
	ytID := ytUrl
	if strings.Contains(ytUrl, "youtu.be/") {
		parts := strings.Split(ytUrl, "youtu.be/")
		ytID = strings.Split(parts[1], "?")[0]
		ytID = strings.Split(ytID, "&")[0]
	} else if strings.Contains(ytUrl, "v=") {
		parts := strings.Split(ytUrl, "v=")
		ytID = strings.Split(parts[1], "&")[0]
	}

	// Cache check
	mu.RLock()
	if song, ok := db[ytID]; ok && song.FileID != "" {
		mu.RUnlock()
		// Telegram se redirect
		token := os.Getenv("BOT_TOKEN")
		filePath := song.FilePath
		if filePath == "" {
			filePath = getTelegramFilePath(song.FileID)
		}
		if token != "" && filePath != "" {
			direct := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, filePath)
			http.Redirect(w, r, direct, 302)
			return
		}
	}
	mu.RUnlock()

	// Naya download - semaphore (512 MB pe 1 only)
	sem <- struct{}{}
	defer func() { <-sem }()

	tmpFile := filepath.Join("/tmp", ytID+".mp3")
	ytArgs := []string{"-x", "--audio-format", "mp3", "--no-playlist", "--no-check-certificate", "--extractor-args", "youtube:player_client=android,web", "-o", tmpFile, ytUrl}
	ytArgs = append(getCookiesArg(), ytArgs...)
	cmd := exec.Command("yt-dlp", ytArgs...)
	out, err := cmd.CombinedOutput()
	log.Printf("yt-dlp: %s", string(out))
	if err != nil {
		http.Error(w, fmt.Sprintf("yt-dlp failed: %s", string(out)), 500)
		return
	}

	// Telegram pe upload
	token := os.Getenv("BOT_TOKEN")
	chatID := os.Getenv("CHANNEL_ID")
	if token != "" && chatID != "" {
		// upload async? yaha sync karte hai pehli baar
		file, _ := os.Open(tmpFile)
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, _ := writer.CreateFormFile("audio", filepath.Base(tmpFile))
		io.Copy(part, file)
		file.Close()
		writer.WriteField("chat_id", chatID)
		writer.WriteField("caption", ytID)
		writer.Close()

		url := fmt.Sprintf("https://api.telegram.org/bot%s/sendAudio", token)
		req, _ := http.NewRequest("POST", url, body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
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

	// Ab jo file hai wahi serve karo
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

	log.Println("Listening on 0.0.0.0:" + port)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}
