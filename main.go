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

func handler(w http.ResponseWriter, r *http.Request) {
	ytUrl := r.URL.Query().Get("url")
	if ytUrl == "" {
		w.Write([]byte("use: /convert?url=YOUTUBE_URL"))
		return
	}

	tmpDir := "/tmp"
	id := fmt.Sprintf("%d", os.Getpid())
	inputTemplate := filepath.Join(tmpDir, id+"_%(title)s.%(ext)s")
	outputMp3 := filepath.Join(tmpDir, id+"_output.mp3")

	// Step 1: yt-dlp se audio nikalo - apne khud ke video / no-copyright ke liye
	// yt-dlp -x --audio-format mp3 -o "/tmp/..." URL
	dlCmd := exec.Command("yt-dlp", "-x", "--audio-format", "mp3", "-o", inputTemplate, ytUrl)
	dlCmd.Stdout = os.Stdout
	dlCmd.Stderr = os.Stderr
	if err := dlCmd.Run(); err!= nil {
		http.Error(w, "yt-dlp failed: "+err.Error(), 500)
		return
	}

	// Step 2: ffmpeg se trim / normalize - 4.5 min ka gaana kaatna hai to?start=30&duration=270
	start := r.URL.Query().Get("start") // e.g. 30
	duration := r.URL.Query().Get("duration") // e.g. 270 for 4.5 min

	// input file dhundo
	matches, _ := filepath.Glob(filepath.Join(tmpDir, id+"_*.mp3"))
	if len(matches) == 0 {
		http.Error(w, "downloaded file not found", 500)
		return
	}
	inputFile := matches[0]

	args := []string{}
	if start!= "" {
		args = append(args, "-ss", start)
	}
	if duration!= "" {
		args = append(args, "-t", duration)
	}
	args = append(args, "-i", inputFile, "-c:a", "libmp3lame", "-b:a", "192k", "-filter:a", "loudnorm", outputMp3)

	if err := runFFmpeg(args...); err!= nil {
		http.Error(w, "ffmpeg failed: "+err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition", "attachment; filename=song.mp3")
	http.ServeFile(w, r, outputMp3)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	http.HandleFunc("/", handler)
	http.HandleFunc("/convert", handler)

	log.Println("Listening on 0.0.0.0:" + port)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}