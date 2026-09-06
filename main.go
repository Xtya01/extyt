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

func tryClient(videoId, clientName, clientVersion, userAgent string, extra map[string]interface{}) (cacheVal, error) {
	clientMap := map[string]interface{}{"clientName": clientName, "clientVersion": clientVersion}
	for k,v := range extra { clientMap[k]=v }
	payload := map[string]interface{}{
		"context": map[string]interface{}{
			"client": clientMap,
			"thirdParty": map[string]interface{}{"embedUrl": fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoId)},
		},
		"videoId": videoId,
		"contentCheckOk": true,
		"racyCheckOk": true,
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
	if ps,ok:=data["playabilityStatus"].(map[string]interface{}); ok {
		if status,ok:=ps["status"].(string); ok && status!="OK" {
			reason,_:=ps["reason"].(string)
			if _,has:=data["streamingData"]; !has {
				return cacheVal{}, fmt.Errorf("%s: %s (%s)", clientName, status, reason)
			}
		}
	}
	sd,ok:=data["streamingData"].(map[string]interface{})
	if !ok { return cacheVal{}, fmt.Errorf("no streamingData with %s", clientName) }
	af,ok:=sd["adaptiveFormats"].([]interface{})
	if !ok { if f,ok:=sd["formats"].([]interface{}); ok { af=f } else { return cacheVal{}, fmt.Errorf("no formats with %s", clientName) } }
	var best string
	for _,f:=range af {
		fm,ok:=f.(map[string]interface{})
		if !ok { continue }
		if itag,ok:=fm["itag"].(float64); ok && itag==140 {
			if u,ok:=fm["url"].(string); ok && u!="" { best=u; break }
		}
	}
	if best=="" {
		for _,f:=range af {
			fm,ok:=f.(map[string]interface{})
			if !ok { continue }
			if mime,ok:=fm["mimeType"].(string); ok && len(mime)>=5 && mime[:5]=="audio" {
				if u,ok:=fm["url"].(string); ok && u!="" { best=u; break }
			}
		}
	}
	if best=="" { return cacheVal{}, fmt.Errorf("no audio with %s", clientName) }
	title:=""
	if vd,ok:=data["videoDetails"].(map[string]interface{}); ok { if t,ok:=vd["title"].(string); ok { title=t } }
	return cacheVal{URL: best, Title: title}, nil
}

func tryScrape(videoId string) (cacheVal, error) {
	url := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoId)
	req,_:=http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent","Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept-Language","en-US,en;q=0.9")
	client:=&http.Client{Timeout:15*time.Second}
	resp,err:=client.Do(req)
	if err!=nil { return cacheVal{}, err }
	defer resp.Body.Close()
	bodyBytes,_:=io.ReadAll(resp.Body)
	body:=string(bodyBytes)
	reUrl := regexp.MustCompile(`"url":"(https:\\/\\/[^"]+googlevideo[^"]+)"`)
	urls := reUrl.FindAllStringSubmatch(body, 20)
	for _,m:=range urls {
		if len(m)>=2 {
			raw := strings.ReplaceAll(m[1], `\/`, "/")
			raw = strings.ReplaceAll(raw, `&`, "&")
			if strings.Contains(raw, "mime=audio") || strings.Contains(raw, "itag=140") || strings.Contains(raw, "itag=139") {
				return cacheVal{URL: raw, Title: "scraped"}, nil
			}
		}
	}
	if len(urls)>0 {
		raw := strings.ReplaceAll(urls[0][1], `\/`, "/")
		raw = strings.ReplaceAll(raw, `&`, "&")
		return cacheVal{URL: raw, Title: "scraped"}, nil
	}
	return cacheVal{}, fmt.Errorf("scrape no url")
}

func tryYtDlp(videoId string) (cacheVal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"--no-playlist",
		"--no-warnings",
		"--no-check-certificate",
		"--extractor-args", "youtube:player_client=android_music,web",
		"-f", "140/bestaudio[ext=m4a]/bestaudio/best",
		"--get-url",
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
	url := strings.TrimSpace(out.String())
	lines := strings.Split(url, "\n")
	first := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(first, "http") {
		return cacheVal{}, fmt.Errorf("yt-dlp invalid url: %s", first)
	}
	return cacheVal{URL: first, Title: "yt-dlp"}, nil
}

func getDirectURL(videoId string) (cacheVal, error) {
	if v,ok:=cache.Load(videoId); ok { return v.(cacheVal), nil }
	clients := []struct{Name, Version, UA string; Extra map[string]interface{}}{
		{"ANDROID_MUSIC","6.20","com.google.android.apps.youtube.music/6.20", map[string]interface{}{"androidSdkVersion":30}},
		{"ANDROID","19.09.37","com.google.android.youtube/19.09.37 (Linux; U; Android 11) gzip", map[string]interface{}{"osName":"Android","osVersion":"11","androidSdkVersion":30}},
		{"IOS","19.09.3","com.google.ios.youtube/19.09.3 (iPhone14,3; U; CPU iOS 15_6 like Mac OS X)", map[string]interface{}{"osName":"iOS","osVersion":"15.6.0.19G71"}},
	}
	var lastErr error
	for _,c:=range clients {
		val,err:=tryClient(videoId, c.Name, c.Version, c.UA, c.Extra)
		if err==nil { cache.Store(videoId,val); return val,nil }
		lastErr=err
		log.Printf("client %s failed: %v", c.Name, err)
	}
	if val,err:=tryScrape(videoId); err==nil {
		cache.Store(videoId,val)
		return val,nil
	} else { log.Printf("scrape failed: %v", err); lastErr=err }
	if val,err:=tryYtDlp(videoId); err==nil {
		log.Printf("yt-dlp success for %s", videoId)
		cache.Store(videoId,val)
		return val,nil
	} else { log.Printf("yt-dlp failed: %v", err); lastErr=err }
	return cacheVal{}, lastErr
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
		json.NewEncoder(w).Encode(map[string]string{"status":"Go+yt-dlp v6 - final","usage":"/api/extract?id=VIDEO_ID"})
	})
	log.Printf("Extractor v6 (yt-dlp) running on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
