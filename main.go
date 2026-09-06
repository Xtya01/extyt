package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
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
			"client": map[string]string{"clientName": clientName, "clientVersion": clientVersion},
		},
		"videoId": videoId,
	}
	b,_:=json.Marshal(payload)
	req,_:=http.NewRequest("POST","https://www.youtube.com/youtubei/v1/player?key=AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8", bytes.NewReader(b))
	req.Header.Set("Content-Type","application/json")
	req.Header.Set("User-Agent",userAgent)
	client:=&http.Client{Timeout:10*time.Second}
	resp,err:=client.Do(req)
	if err!=nil { return cacheVal{}, err }
	defer resp.Body.Close()
	var data map[string]interface{}
	if err:=json.NewDecoder(resp.Body).Decode(&data); err!=nil { return cacheVal{}, err }
	sd,ok:=data["streamingData"].(map[string]interface{})
	if !ok { return cacheVal{}, fmt.Errorf("no streamingData with %s", clientName) }
	af,ok:=sd["adaptiveFormats"].([]interface{})
	if !ok { return cacheVal{}, fmt.Errorf("no formats") }
	var best string
	for _,f:=range af {
		fm:=f.(map[string]interface{})
		if itag,ok:=fm["itag"].(float64); ok && itag==140 {
			if u,ok:=fm["url"].(string); ok { best=u; break }
		}
	}
	if best=="" {
		for _,f:=range af {
			fm:=f.(map[string]interface{})
			if mime,ok:=fm["mimeType"].(string); ok && len(mime)>=5 && mime[:5]=="audio" {
				if u,ok:=fm["url"].(string); ok { best=u; break }
			}
		}
	}
	if best=="" { return cacheVal{}, fmt.Errorf("no audio") }
	title:=""
	if vd,ok:=data["videoDetails"].(map[string]interface{}); ok { if t,ok:=vd["title"].(string); ok { title=t } }
	return cacheVal{URL: best, Title: title}, nil
}

func tryYtDlp(videoId string) (cacheVal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// FIXED: use universal format, no restrictive 140, no extractor-args
	// yt-dlp will auto pick best client
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"--no-playlist",
		"--no-warnings",
		"-f", "bestaudio/best",
		"--get-url",
		"--no-check-certificate",
		fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoId),
	)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err!=nil {
		return cacheVal{}, fmt.Errorf("yt-dlp failed: %v | stderr: %s", err, errBuf.String())
	}
	urlStr := strings.TrimSpace(out.String())
	lines := strings.Split(urlStr, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "http") {
			return cacheVal{URL: l, Title: "yt-dlp"}, nil
		}
	}
	return cacheVal{}, fmt.Errorf("yt-dlp empty")
}

func tryPiped(videoId string) (cacheVal, error) {
	bases := []string{"https://pipedapi.kavin.rocks","https://pipedapi.moomoo.me"}
	for _, base := range bases {
		url := fmt.Sprintf("%s/streams/%s", base, videoId)
		client:=&http.Client{Timeout:10*time.Second}
		resp,err:=client.Get(url)
		if err!=nil { continue }
		if resp.StatusCode!=200 { resp.Body.Close(); continue }
		var data map[string]interface{}
		if err:=json.NewDecoder(resp.Body).Decode(&data); err!=nil { resp.Body.Close(); continue }
		resp.Body.Close()
		if audios,ok:=data["audioStreams"].([]interface{}); ok && len(audios)>0 {
			if am,ok:=audios[0].(map[string]interface{}); ok {
				if u,ok:=am["url"].(string); ok {
					title,_:=data["title"].(string)
					return cacheVal{URL: u, Title: title}, nil
				}
			}
		}
	}
	return cacheVal{}, fmt.Errorf("piped failed")
}

func getDirectURL(videoId string) (cacheVal, error) {
	if v,ok:=cache.Load(videoId); ok { return v.(cacheVal), nil }

	// try simple clients first (fast)
	for _, c := range []struct{Name,Ver,UA string}{
		{"ANDROID_MUSIC","6.20","com.google.android.apps.youtube.music/6.20"},
		{"ANDROID","19.09.37","com.google.android.youtube/19.09.37 (Linux; U; Android 11)"},
	} {
		if val,err:=tryClient(videoId,c.Name,c.Ver,c.UA); err==nil {
			cache.Store(videoId,val); return val,nil
		}
	}

	// yt-dlp final (most reliable)
	if val,err:=tryYtDlp(videoId); err==nil {
		cache.Store(videoId,val); return val,nil
	} else {
		log.Printf("yt-dlp failed: %v", err)
		// fallback piped
		if val2,err2:=tryPiped(videoId); err2==nil {
			cache.Store(videoId,val2); return val2,nil
		}
		return cacheVal{}, err
	}
}

func extractHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin","*")
	w.Header().Set("Content-Type","application/json")
	id:=r.URL.Query().Get("id")
	if id=="" { w.WriteHeader(400); json.NewEncoder(w).Encode(map[string]string{"error":"id missing"}); return }
	result,err:=getDirectURL(id)
	if err!=nil { w.WriteHeader(500); json.NewEncoder(w).Encode(map[string]string{"error":err.Error()}); return }
	json.NewEncoder(w).Encode(result)
}

func main() {
	port:=os.Getenv("PORT")
	if port=="" { port="8000" }
	http.HandleFunc("/api/extract", extractHandler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request){
		w.Header().Set("Content-Type","application/json")
		json.NewEncoder(w).Encode(map[string]string{"status":"Go+yt-dlp v7 fixed format","usage":"/api/extract?id=VIDEO_ID"})
	})
	log.Printf("Extractor v7 running on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
