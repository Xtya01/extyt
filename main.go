package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

var cache sync.Map

type cacheVal struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

func getDirectURL(videoId string) (cacheVal, error) {
	if v, ok := cache.Load(videoId); ok {
		return v.(cacheVal), nil
	}

	// ANDROID_MUSIC client - sabse halka, Vercel jaisa IP block nahi
	payload := map[string]interface{}{
		"context": map[string]interface{}{
			"client": map[string]string{
				"clientName":    "ANDROID_MUSIC",
				"clientVersion": "6.20",
			},
		},
		"videoId": videoId,
	}
	b, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", "https://www.youtube.com/youtubei/v1/player?key=AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "com.google.android.apps.youtube.music/6.20")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return cacheVal{}, err
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return cacheVal{}, err
	}

	// streamingData -> adaptiveFormats me audio hota hai
	sd, ok := data["streamingData"].(map[string]interface{})
	if !ok {
		return cacheVal{}, fmt.Errorf("no streamingData - video blocked or private")
	}
	af, ok := sd["adaptiveFormats"].([]interface{})
	if !ok {
		return cacheVal{}, fmt.Errorf("no formats")
	}

	var bestURL string
	// itag 140 = m4a 128k best for low cpu
	for _, f := range af {
		fm := f.(map[string]interface{})
		if itag, ok := fm["itag"].(float64); ok && itag == 140 {
			if u, ok := fm["url"].(string); ok {
				bestURL = u
				break
			}
		}
	}
	if bestURL == "" {
		// fallback koi bhi audio
		for _, f := range af {
			fm := f.(map[string]interface{})
			if mime, ok := fm["mimeType"].(string); ok && len(mime) > 5 && mime[:5] == "audio" {
				if u, ok := fm["url"].(string); ok {
					bestURL = u
					break
				}
			}
		}
	}
	if bestURL == "" {
		return cacheVal{}, fmt.Errorf("no audio url found")
	}

	title := ""
	if vd, ok := data["videoDetails"].(map[string]interface{}); ok {
		if t, ok := vd["title"].(string); ok {
			title = t
		}
	}

	val := cacheVal{URL: bestURL, Title: title}
	cache.Store(videoId, val)
	return val, nil
}

func extractHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	id := r.URL.Query().Get("id")
	if id == "" {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "id missing, use ?id=VIDEO_ID"})
		return
	}

	result, err := getDirectURL(id)
	if err != nil {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(result)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000" // Koyeb default
	}
	http.HandleFunc("/api/extract", extractHandler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "Go extractor running - RAM <15MB",
			"usage":  "/api/extract?id=VIDEO_ID",
		})
	})

	log.Printf("Extractor running on :%s - RAM <15MB", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}