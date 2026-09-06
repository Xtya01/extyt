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

func tryClient(videoId, clientName, clientVersion, userAgent string, extra map[string]interface{}) (cacheVal, error) {
	clientMap := map[string]interface{}{
		"clientName": clientName,
		"clientVersion": clientVersion,
	}
	// extra fields like osName, androidSdkVersion etc
	for k,v := range extra {
		clientMap[k] = v
	}

	payload := map[string]interface{}{
		"context": map[string]interface{}{
			"client": clientMap,
			"thirdParty": map[string]interface{}{
				"embedUrl": fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoId),
			},
		},
		"videoId": videoId,
		"contentCheckOk": true,
		"racyCheckOk": true,
	}

	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "https://www.youtube.com/youtubei/v1/player?key=AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Youtube-Client-Name", "1")
	req.Header.Set("X-Youtube-Client-Version", clientVersion)

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return cacheVal{}, err
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return cacheVal{}, err
	}

	// playability check for debug
	if ps, ok := data["playabilityStatus"].(map[string]interface{}); ok {
		if status, ok := ps["status"].(string); ok && status != "OK" {
			reason, _ := ps["reason"].(string)
			// still try streamingData, but save reason
			if _, hasSD := data["streamingData"]; !hasSD {
				return cacheVal{}, fmt.Errorf("%s: %s (%s)", clientName, status, reason)
			}
		}
	}

	sd, ok := data["streamingData"].(map[string]interface{})
	if !ok {
		return cacheVal{}, fmt.Errorf("no streamingData with %s", clientName)
	}
	af, ok := sd["adaptiveFormats"].([]interface{})
	if !ok {
		// also check formats
		if f, ok := sd["formats"].([]interface{}); ok {
			af = f
		} else {
			return cacheVal{}, fmt.Errorf("no formats with %s", clientName)
		}
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
			if mime, ok := fm["mimeType"].(string); ok && len(mime) >=5 && mime[:5]=="audio" {
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

	clients := []struct{
		Name, Version, UA string
		Extra map[string]interface{}
	}{
		{"ANDROID_MUSIC", "6.20", "com.google.android.apps.youtube.music/6.20", map[string]interface{}{"androidSdkVersion": 30}},
		{"ANDROID", "19.09.37", "com.google.android.youtube/19.09.37 (Linux; U; Android 11) gzip", map[string]interface{}{"osName":"Android","osVersion":"11","androidSdkVersion":30}},
		{"IOS", "19.09.3", "com.google.ios.youtube/19.09.3 (iPhone14,3; U; CPU iOS 15_6 like Mac OS X)", map[string]interface{}{"osName":"iOS","osVersion":"15.6.0.19G71","deviceMake":"Apple","deviceModel":"iPhone14,3"}},
		{"WEB", "2.20231219.04.00", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36", nil},
		{"WEB_EMBEDDED_PLAYER", "1.20240723.01.00", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)", map[string]interface{}{"clientScreen":"EMBED"}},
		{"MWEB", "2.20231219.04.00", "Mozilla/5.0 (iPhone; CPU iPhone OS 15_6 like Mac OS X)", nil},
	}

	var lastErr error
	for _, c := range clients {
		val, err := tryClient(videoId, c.Name, c.Version, c.UA, c.Extra)
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
		json.NewEncoder(w).Encode(map[string]string{"error": "id missing ?id=VIDEO_ID"})
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
	if port == "" { port = "8000" }
	http.HandleFunc("/api/extract", extractHandler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "Go extractor v4 - IOS+EMBED bypass",
			"usage": "/api/extract?id=VIDEO_ID",
		})
	})
	log.Printf("Extractor v4 running on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
