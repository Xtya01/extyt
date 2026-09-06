package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

func runFFmpeg(args...string) (string, error) {
	cmd := exec.Command("ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	log.Printf("ffmpeg out: %s", string(out))
	return string(out), err
}

func health(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("OK - extyt running v3"))
}

func handler(w http.ResponseWriter, r *http.Request) {
	ytUrl := r.URL.Query().Get("url")
	if ytUrl == "" {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Use: /convert?url=YOUTUBE_URL"))
		return
	}

	tmpDir := "/tmp"
	id := fmt.Sprintf("%d", os.Getpid())
	// yt-dlp ke liye template
	inputTemplate := filepath.Join(tmpDir, id+"_%(title)s.%(ext)s")
	outputMp3 := filepath.Join(tmpDir, id+"_output.mp3")

	log.Println("Downloading:", ytUrl)
	// Fix 1: latest client spoof - YouTube ne web client block kiya hua hai
	// android client + web fallback sabse stable hai 2024-2026 me
	ytDlpArgs := []string{
		"-x", "--audio-format", "mp3",
		"--no-playlist",
		"--no-check-certificate",
		"--extractor-args", "youtube:player_client=android,web",
		"--user-agent", "Mozilla/5.0 (Linux; Android 10) AppleWebKit/537.36",
		"-o", inputTemplate,
		ytUrl,
	}
	cmd := exec.Command("yt-dlp", ytDlpArgs...)
	out, err := cmd.CombinedOutput()
	log.Printf("yt-dlp output: %s", string(out))
	if err != nil {
		http.Error(w, fmt.Sprintf("yt-dlp failed: %v\n\nFull Log:\n%s", err, string(out)), 500)
		return
	}

	matches, _ := filepath.Glob(filepath.Join(tmpDir, id+"_*.mp3"))
	if len(matches) == 0 {
		m1, _ := filepath.Glob(filepath.Join(tmpDir, id+"_*.m4a"))
		m2, _ := filepath.Glob(filepath.Join(tmpDir, id+"_*.webm"))
		m3, _ := filepath.Glob(filepath.Join(tmpDir, id+"_*.opus"))
		matches = append(m1, m2...)
		matches = append(matches, m3...)
		if len(matches) == 0 {
			http.Error(w, fmt.Sprintf("File not found after download. yt-dlp log:\n%s", string(out)), 500)
			return
		}
	}
	inputFile := matches[0]

	start := r.URL.Query().Get("start")
	duration := r.URL.Query().Get("duration")

	args := []string{}
	if start != "" {
		args = append(args, "-ss", start)
	}
	if duration != "" {
		args = append(args, "-t", duration)
	}
	args = append(args, "-i", inputFile, "-c:a", "libmp3lame", "-b:a", "192k", "-filter:a", "loudnorm", "-y", outputMp3)

	ffmpegLog, err := runFFmpeg(args...)
	if err != nil {
		http.Error(w, fmt.Sprintf("ffmpeg failed: %v\nLog: %s", err, ffmpegLog), 500)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition", "attachment; filename=song.mp3")
	http.ServeFile(w, r, outputMp3)

	go func() {
		os.Remove(outputMp3)
		for _, f := range matches {
			os.Remove(f)
		}
	}()
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	http.HandleFunc("/", health)
	http.HandleFunc("/health", health)
	http.HandleFunc("/convert", handler)

	log.Println("Listening on 0.0.0.0:" + port)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}
