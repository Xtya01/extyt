package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
)

func runFFmpeg(args...string) error {
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	log.Println("Running:", cmd.String())
	return cmd.Run()
}

func health(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("OK - extyt running"))
}

func handler(w http.ResponseWriter, r *http.Request) {
	ytUrl := r.URL.Query().Get("url")
	if ytUrl == "" {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Use: /convert?url=YOUTUBE_URL\nExample: /convert?url=https://www.youtube.com/watch?v=BaW_jenozKc"))
		return
	}

	tmpDir := "/tmp"
	id := fmt.Sprintf("%d-%d", os.Getpid(), os.Getuid())
	inputTemplate := filepath.Join(tmpDir, id+"_%(title)s.%(ext)s")
	outputMp3 := filepath.Join(tmpDir, id+"_output.mp3")

	log.Println("Downloading:", ytUrl)
	dlCmd := exec.Command("yt-dlp", "-x", "--audio-format", "mp3", "-o", inputTemplate, ytUrl)
	dlCmd.Stdout = os.Stdout
	dlCmd.Stderr = os.Stderr
	if err := dlCmd.Run(); err != nil {
		http.Error(w, "yt-dlp failed: "+err.Error(), 500)
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
			http.Error(w, "downloaded file not found", 500)
			return
		}
	}
	inputFile := matches[0]
	log.Println("Input file:", inputFile)

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

	if err := runFFmpeg(args...); err != nil {
		http.Error(w, "ffmpeg failed: "+err.Error(), 500)
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
