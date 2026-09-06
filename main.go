package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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
	clientMap := map[string]interface{}{
		"clientName": clientName,
		"clientVersion": clientVersion,
	}
	for k,v := range extra {
		clientMap[k]=v
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
	b,_:=json.Marshal(payload)
	req,_:=http.NewRequest("POST","https://www.youtube.com/youtubei/v1/player?key=AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8", bytes.NewReader(b))
	req.Header.Set("Content-Type","application/json")
	req.Header.Set("User-Agent",userAgent)

	client:=&http.Client{Timeout:12*time.Second}
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
	if !ok {
		if f,ok:=sd["formats"].([]interface{}); ok { af=f } else { return cacheVal{}, fmt.Errorf("no formats with %s", clientName) }
	}

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
	if best=="" { return cacheVal{}, fmt.Errorf("no audio url with %s", clientName) }

	title:=""
	if vd,ok:=data["videoDetails"].(map[string]interface{}); ok {
		if t,ok:=vd["title"].(string); ok { title=t }
	}
	return cacheVal{URL: best, Title: title}, nil
}

func tryScrape(videoId string) (cacheVal, error) {
	url := fmt.Sprintf("https://www.youtube.com/watch?v=%s&hl=en&has_verified=1&bpctr=9999999999", videoId)
	req,_:=http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent","Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language","en-US,en;q=0.9")
	req.Header.Set("Accept","text/html,application/xhtml+xml")
	client:=&http.Client{Timeout:15*time.Second}
	resp,err:=client.Do(req)
	if err!=nil { return cacheVal{}, err }
	defer resp.Body.Close()
	bodyBytes,_:=io.ReadAll(resp.Body)
	body:=string(bodyBytes)

	// Find ytInitialPlayerResponse
	re := regexp.MustCompile(`ytInitialPlayerResponse\s*=\s*(\{.*?\});`)
	matches := re.FindStringSubmatch(body)
	var jsonStr string
	if len(matches)>=2 {
		jsonStr = matches[1]
	} else {
		// fallback: look for var ytInitialPlayerResponse = 
		idx := strings.Index(body, `"streamingData"`)
		if idx==-1 { return cacheVal{}, fmt.Errorf("scrape: no streamingData in page") }
		// grab 100k chars around
		start := idx-5000
		if start<0 { start=0 }
		end := idx+20000
		if end>len(body) { end=len(body) }
		snippet := body[start:end]
		// find urls
		reUrl := regexp.MustCompile(`"url":"(https:\/\/[^"]+googlevideo[^"]+)"`)
		urls := reUrl.FindAllStringSubmatch(snippet, 10)
		if len(urls)>0 {
			raw := urls[0][1]
			raw = strings.ReplaceAll(raw, `\/`, `/`)
			raw = strings.ReplaceAll(raw, `&`, "&")
			return cacheVal{URL: raw, Title: "scraped"}, nil
		}
		return cacheVal{}, fmt.Errorf("scrape: no url found")
	}

	var data map[string]interface{}
	if err:=json.Unmarshal([]byte(jsonStr), &data); err!=nil {
		return cacheVal{}, fmt.Errorf("scrape json parse: %v", err)
	}
	sd,ok:=data["streamingData"].(map[string]interface{})
	if !ok { return cacheVal{}, fmt.Errorf("scrape: no streamingData") }
	af,ok:=sd["adaptiveFormats"].([]interface{})
	if !ok { return cacheVal{}, fmt.Errorf("scrape: no adaptiveFormats") }
	for _,f:=range af {
		fm:=f.(map[string]interface{})
		if itag,ok:=fm["itag"].(float64); ok && itag==140 {
			if u,ok:=fm["url"].(string); ok { return cacheVal{URL: u, Title: "scraped"}, nil }
		}
	}
	for _,f:=range af {
		fm:=f.(map[string]interface{})
		if mime,ok:=fm["mimeType"].(string); ok && strings.HasPrefix(mime,"audio") {
			if u,ok:=fm["url"].(string); ok { return cacheVal{URL: u, Title: "scraped"}, nil }
		}
	}
	return cacheVal{}, fmt.Errorf("scrape: no audio")
}

func tryPiped(videoId string) (cacheVal, error) {
	// Piped API - works even when YouTube blocks datacenter IP
	// try multiple instances
	instances := []string{
		"https://pipedapi.kavin.rocks",
		"https://pipedapi.moomoo.me",
		"https://api.piped.private.coffee",
	}
	var lastErr error
	for _, base := range instances {
		url := fmt.Sprintf("%s/streams/%s", base, videoId)
		client:=&http.Client{Timeout:12*time.Second}
		resp,err:=client.Get(url)
		if err!=nil { lastErr=err; continue }
		if resp.StatusCode!=200 { lastErr=fmt.Errorf("piped %s status %d", base, resp.StatusCode); resp.Body.Close(); continue }
		var data map[string]interface{}
		if err:=json.NewDecoder(resp.Body).Decode(&data); err!=nil { resp.Body.Close(); lastErr=err; continue }
		resp.Body.Close()
		// audioStreams
		if audios,ok:=data["audioStreams"].([]interface{}); ok {
			// find itag 140
			for _,a:=range audios {
				am:=a.(map[string]interface{})
				if itag,ok:=am["itag"].(float64); ok && itag==140 {
					if u,ok:=am["url"].(string); ok { return cacheVal{URL: u, Title: data["title"].(string)}, nil }
				}
			}
			// any audio
			if len(audios)>0 {
				if am,ok:=audios[0].(map[string]interface{}); ok {
					if u,ok:=am["url"].(string); ok { 
						title,_:=data["title"].(string)
						return cacheVal{URL: u, Title: title}, nil 
					}
				}
			}
		}
		lastErr=fmt.Errorf("piped %s no audioStreams", base)
	}
	return cacheVal{}, lastErr
}

func getDirectURL(videoId string) (cacheVal, error) {
	if v,ok:=cache.Load(videoId); ok { return v.(cacheVal), nil }

	clients := []struct{Name, Version, UA string; Extra map[string]interface{}}{
		{"ANDROID_MUSIC","6.20","com.google.android.apps.youtube.music/6.20", map[string]interface{}{"androidSdkVersion":30}},
		{"ANDROID","19.09.37","com.google.android.youtube/19.09.37 (Linux; U; Android 11) gzip", map[string]interface{}{"osName":"Android","osVersion":"11","androidSdkVersion":30}},
		{"IOS","19.09.3","com.google.ios.youtube/19.09.3 (iPhone14,3; U; CPU iOS 15_6 like Mac OS X)", map[string]interface{}{"osName":"iOS","osVersion":"15.6.0.19G71"}},
		{"WEB_EMBEDDED_PLAYER","1.20240723.01.00","Mozilla/5.0 (Windows NT 10.0; Win64; x64)", map[string]interface{}{"clientScreen":"EMBED"}},
	}

	var lastErr error
	for _,c:=range clients {
		val,err:=tryClient(videoId, c.Name, c.Version, c.UA, c.Extra)
		if err==nil { cache.Store(videoId,val); return val,nil }
		lastErr=err
		log.Printf("client %s failed: %v", c.Name, err)
	}

	// scrape fallback
	if val,err:=tryScrape(videoId); err==nil {
		log.Printf("scrape success for %s", videoId)
		cache.Store(videoId,val)
		return val,nil
	} else {
		log.Printf("scrape failed: %v", err)
		lastErr=err
	}

	// piped fallback - final
	if val,err:=tryPiped(videoId); err==nil {
		log.Printf("piped success for %s", videoId)
		cache.Store(videoId,val)
		return val,nil
	} else {
		log.Printf("piped failed: %v", err)
		lastErr=err
	}

	return cacheVal{}, lastErr
}

func extractHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin","*")
	w.Header().Set("Content-Type","application/json")
	id:=r.URL.Query().Get("id")
	if id=="" {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error":"id missing"})
		return
	}
	result,err:=getDirectURL(id)
	if err!=nil {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"error":err.Error()})
		return
	}
	json.NewEncoder(w).Encode(result)
}

func main() {
	port:=os.Getenv("PORT")
	if port=="" { port="8000" }
	http.HandleFunc("/api/extract", extractHandler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request){
		w.Header().Set("Content-Type","application/json")
		json.NewEncoder(w).Encode(map[string]string{"status":"Go extractor v5 - piped fallback","usage":"/api/extract?id=VIDEO_ID"})
	})
	log.Printf("Extractor v5 running on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
