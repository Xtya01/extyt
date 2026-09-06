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

func tryClient(videoId, clientName, clientVersion, userAgent string) (cacheVal, error) {
	payload := map[string]interface{}{
		"context": map[string]interface{}{
			"client": map[string]string{
				"clientName": clientName,
				"clientVersion": clientVersion,
			},
		},
		"videoId": videoId,
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "https://www.youtube.com/youtubei/v1/player?key=AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

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

	sd, ok := data["streamingData"].(map[string]interface{})
	if !ok {
		return cacheVal{}, fmt.Errorf("no streamingData with %s", clientName)
	}
	af, ok := sd["adaptiveFormats"].([]interface{})
	if !ok {
		return cacheVal{}, fmt.Errorf("no formats with %s", clientName)
	}

	var bestURL string
	for _, f := range af {
		fm, ok := f.(map[string]interface{})
		if !ok { continue }
		if itag, ok := fm["itag"].(float64); ok && itag == 140 {
			if u, ok := fm["url"].(string); ok && u != "" {
				bestURL = u
				break
			}
		}
	}
	if bestURL == "" {
		for _, f := range af {
			fm, ok := f.(map[string]interface{})
			if !ok { continue }
			if mime, ok := fm["mimeType"].(string); ok && len(mime) >= 5 && mime[:5] == "audio" {
				if u, ok := fm["url"].(string); ok && u != "" {
					bestURL = u
					break
				}
			}
		}
	}
	if bestURL == "" {
		return cacheVal{}, fmt.Errorf("no audio url with %s", clientName)
	}

	title := ""
	if vd, ok := data["videoDetails"].(map[string]interface{}); ok {
		if t, ok := vd["title"].(string); ok {
			title = t
		}
	}
	return cacheVal{URL: bestURL, Title: title}, nil
}

func getDirectURL(videoId string) (cacheVal, error) {
	if v, ok := cache.Load(videoId); ok {
		return v.(cacheVal), nil
	}

	clients := []struct{ Name, Version, UA string }{
		{"ANDROID_MUSIC", "6.20", "com.google.android.apps.youtube.music/6.20"},
		{"ANDROID", "19.09.37", "com.google.android.youtube/19.09.37 (Linux; U; Android 11)"},
		{"WEB", "2.20231219.04.00", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"},
	}

	var lastErr error
	for _, c := range clients {
		val, err := tryClient(videoId, c.Name, c.Version, c.UA)
		if err == nil {
			cache.Store(videoId, val)
			return val, nil
		}
		lastErr = err
		log.Printf("client %s failed: %v", c.Name, err)
	}
	return cacheVal{}, lastErr
}

func extractHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	id := r.URL.Query().Get("id")
	if id == "" {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "id missing"})
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
		port = "8000"
	}
	http.HandleFunc("/api/extract", extractHandler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "Go extractor running - multi-client v3",
			"usage": "/api/extract?id=VIDEO_ID",
		})
	})
	log.Printf("Extractor running on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
