// main_koyeb_env_ui.go - Koyeb env hi UI me use hoga, alag alg nahi - PRO v4.1
package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Port string; JioApiUrl string; YtApiKey string; YtCookies string; YtCookiesB64 string
	TelegramToken string; TelegramChatID string; TelegramFileID string; TelegramMusicChatID string
	SubUser string; SubPass string; SubUsersRaw string; DbPath string
}
func env(k,d string)string{ if v:=os.Getenv(k); v!=""{return v}; return d }
func loadConfig() Config{
	c:=Config{
		Port: env("PORT","8000"), JioApiUrl: env("JIOSAAVN_API_URL","https://jiosaavn-api-three-ashy.vercel.app"),
		YtApiKey: os.Getenv("YT_API_KEY"), YtCookies: os.Getenv("YT_COOKIES"), YtCookiesB64: os.Getenv("YT_COOKIES_B64"),
		TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"), TelegramChatID: os.Getenv("TELEGRAM_CHAT_ID"),
		TelegramFileID: os.Getenv("TELEGRAM_DB_FILE_ID"), TelegramMusicChatID: env("TELEGRAM_MUSIC_CHAT_ID",""),
		SubUser: env("SUBSONIC_USER","admin"), SubPass: env("SUBSONIC_PASSWORD","admin"),
		SubUsersRaw: os.Getenv("SUBSONIC_USERS"), DbPath: env("DB_PATH","db.json"),
	}
	c.JioApiUrl=strings.TrimSuffix(c.JioApiUrl,"/")
	if c.TelegramMusicChatID==""{c.TelegramMusicChatID=c.TelegramChatID}
	return c
}
var cfg Config
var usersMap=map[string]string{}
var muUsers sync.RWMutex

func getCookiesContent() string{
	if b64:=os.Getenv("YT_COOKIES_B64"); strings.TrimSpace(b64)!=""{
		trimmed:=strings.TrimSpace(b64)
		if d,err:=base64.StdEncoding.DecodeString(trimmed); err==nil{return string(d)}
		if d,err:=base64.RawStdEncoding.DecodeString(trimmed); err==nil{return string(d)}
		if d,err:=base64.URLEncoding.DecodeString(trimmed); err==nil{return string(d)}
		return trimmed
	}
	raw:=os.Getenv("YT_COOKIES")
	if raw==""{return ""}
	trimmed:=strings.TrimSpace(raw)
	if strings.Contains(trimmed,"Netscape"){return trimmed}
	if d,err:=base64.StdEncoding.DecodeString(trimmed); err==nil{s:=string(d); if strings.Contains(s,"Netscape"){return s}}
	if d,err:=base64.RawStdEncoding.DecodeString(trimmed); err==nil{s:=string(d); if strings.Contains(s,"Netscape"){return s}}
	return trimmed
}
func initUsers(){
	muUsers.Lock(); defer muUsers.Unlock()
	usersMap=map[string]string{}; usersMap[cfg.SubUser]=cfg.SubPass
	if cfg.SubUsersRaw!=""{for _,p:=range strings.Split(cfg.SubUsersRaw,","){p=strings.TrimSpace(p); if p==""{continue}; kv:=strings.SplitN(p,":",2); if len(kv)==2{usersMap[strings.TrimSpace(kv[0])]=strings.TrimSpace(kv[1])}}}
	if db.Users!=nil{for u,pw:=range db.Users{if _,ok:=usersMap[u];!ok{usersMap[u]=pw}}}
}

type Song struct{ID string `json:"id"`; Title string `json:"title"`; Artist string `json:"artist"`; ArtistID string `json:"artistId"`; Album string `json:"album"`; AlbumID string `json:"albumId"`; Duration int `json:"duration"`; CoverArt string `json:"coverArt"`; Year int `json:"year"`; Genre string `json:"genre"`; Source string `json:"source"`; SourceID string `json:"sourceId"`; StreamURL string `json:"streamUrl,omitempty"`; ThumbURL string `json:"thumbUrl"`; PlayCount int `json:"playCount"`; TgFileID string `json:"tgFileId,omitempty"`; TgFileUniqueID string `json:"tgFileUniqueId,omitempty"`; CachedAt string `json:"cachedAt,omitempty"`}
type Artist struct{ID string `json:"id"`; Name string `json:"name"`}
type Album struct{ID string `json:"id"`; Name string `json:"name"`; Artist string `json:"artist"`; ArtistID string `json:"artistId"`; CoverArt string `json:"coverArt"`; SongIDs []string `json:"songIds"`; ThumbURL string `json:"thumbUrl"`; Year int `json:"year"`}
type Playlist struct{ID string `json:"id"`; Name string `json:"name"`; Owner string `json:"owner"`; Comment string `json:"comment"`; SongIDs []string `json:"songIds"`; Public bool `json:"public"`; Created string `json:"created"`; Source string `json:"source"`; SourceURL string `json:"sourceUrl"`}
type DB struct{sync.RWMutex; Songs map[string]*Song `json:"songs"`; Artists map[string]*Artist `json:"artists"`; Albums map[string]*Album `json:"albums"`; Playlists map[string]*Playlist `json:"playlists"`; Users map[string]string `json:"users"`; Starred map[string]map[string]bool `json:"starred"`}
var db=&DB{Songs: make(map[string]*Song), Artists: make(map[string]*Artist), Albums: make(map[string]*Album), Playlists: make(map[string]*Playlist), Users: make(map[string]string), Starred: make(map[string]map[string]bool)}
var streamCache sync.Map
type cachedURL struct{URL string; Expiry time.Time}
var ytSem=make(chan struct{},2)
var tgCacheInProgress sync.Map
var httpClient=&http.Client{Timeout: 10*time.Second}

type ConnStatus struct{
	TG struct{Connected bool; Working bool; Error string; BotName string; ChatID string} `json:"tg"`
	YT struct{Connected bool; Working bool; Error string; HasKey bool; HasCookies bool; YtdlpVersion string} `json:"yt"`
	Jio struct{Connected bool; Working bool; Error string; ApiUrl string} `json:"jio"`
	Cookies struct{Exists bool; Size int; Valid bool} `json:"cookies"`
}
var connCache = ConnStatus{}
var connCacheTime time.Time
var connMu sync.RWMutex

func checkConnections() ConnStatus{
	connMu.RLock()
	if time.Since(connCacheTime) < 30*time.Second {
		c := connCache
		connMu.RUnlock()
		return c
	}
	connMu.RUnlock()
	var status ConnStatus
	if cfg.TelegramToken != "" {
		status.TG.Connected = true
		status.TG.ChatID = cfg.TelegramChatID
		u := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", cfg.TelegramToken)
		resp, err := httpClient.Get(u)
		if err != nil {
			status.TG.Working = false
			status.TG.Error = err.Error()
		} else {
			defer resp.Body.Close()
			var r struct{Ok bool; Result struct{Username string `json:"username"`} `json:"result"`; Description string `json:"description"`}
			json.NewDecoder(resp.Body).Decode(&r)
			if r.Ok {
				status.TG.Working = true
				status.TG.BotName = r.Result.Username
			} else {
				status.TG.Working = false
				status.TG.Error = r.Description
			}
		}
	} else {
		status.TG.Connected = false
		status.TG.Error = "No token"
	}
	if cfg.YtApiKey != "" {
		status.YT.HasKey = true
		status.YT.Connected = true
		testUrl := fmt.Sprintf("https://www.googleapis.com/youtube/v3/search?part=snippet&q=test&maxResults=1&key=%s", cfg.YtApiKey)
		resp, err := httpClient.Get(testUrl)
		if err != nil {
			status.YT.Working = false
			status.YT.Error = err.Error()
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if strings.Contains(string(body), "items") {
				status.YT.Working = true
			} else {
				status.YT.Working = false
				status.YT.Error = string(body[:min(200, len(body))])
			}
		}
	} else {
		status.YT.HasKey = false
		status.YT.Connected = false
		status.YT.Error = "No API key"
	}
	cmd := exec.Command("yt-dlp", "--version")
	out, err := cmd.CombinedOutput()
	if err == nil {
		status.YT.YtdlpVersion = strings.TrimSpace(string(out))
		if status.YT.HasKey && !status.YT.Working && status.YT.YtdlpVersion != "" {
			status.YT.Working = true
			status.YT.Error = "API failed but yt-dlp works"
		}
	}
	cc := getCookiesContent()
	if cc != "" {
		status.YT.HasCookies = true
		status.Cookies.Exists = true
		status.Cookies.Size = len(cc)
		status.Cookies.Valid = strings.Contains(cc, "youtube") || strings.Contains(cc, "Netscape")
	}
	status.Jio.ApiUrl = cfg.JioApiUrl
	if cfg.JioApiUrl != "" {
		status.Jio.Connected = true
		resp, err := httpClient.Get(cfg.JioApiUrl + "/search?query=test")
		if err != nil {
			status.Jio.Working = false
			status.Jio.Error = err.Error()
		} else {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				status.Jio.Working = true
			} else {
				status.Jio.Working = false
				status.Jio.Error = fmt.Sprintf("Status %d", resp.StatusCode)
			}
		}
	}
	connMu.Lock()
	connCache = status
	connCacheTime = time.Now()
	connMu.Unlock()
	return status
}
func min(a,b int)int{ if a<b{return a}; return b }
func (d *DB) save() error{d.RLock(); defer d.RUnlock(); b,_:=json.MarshalIndent(d,"","  "); tmp:=cfg.DbPath+".tmp"; os.WriteFile(tmp,b,0644); return os.Rename(tmp,cfg.DbPath)}
func (d *DB) load() error{if _,err:=os.Stat(cfg.DbPath); err!=nil{return err}; b,_:=os.ReadFile(cfg.DbPath); var tmp DB; json.Unmarshal(b,&tmp); d.Lock(); if tmp.Songs!=nil{d.Songs=tmp.Songs}; if tmp.Artists!=nil{d.Artists=tmp.Artists}; if tmp.Albums!=nil{d.Albums=tmp.Albums}; if tmp.Playlists!=nil{d.Playlists=tmp.Playlists}; if tmp.Users!=nil{d.Users=tmp.Users}; if tmp.Starred!=nil{d.Starred=tmp.Starred}; if d.Starred==nil{d.Starred=make(map[string]map[string]bool)}; d.Unlock(); return nil}
func telegramUploadDB(){if cfg.TelegramToken==""||cfg.TelegramChatID==""{return}; if _,err:=os.Stat(cfg.DbPath); err!=nil{return}; f,_:=os.Open(cfg.DbPath); defer f.Close(); body:=&bytes.Buffer{}; w:=multipart.NewWriter(body); w.WriteField("chat_id",cfg.TelegramChatID); w.WriteField("caption",fmt.Sprintf("backup %s songs:%d pls:%d users:%d cached:%d",time.Now().Format(time.RFC3339),len(db.Songs),len(db.Playlists),len(db.Users),countCached())); part,_:=w.CreateFormFile("document","db.json"); io.Copy(part,f); w.Close(); url:=fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument",cfg.TelegramToken); req,_:=http.NewRequest("POST",url,body); req.Header.Set("Content-Type",w.FormDataContentType()); client:=&http.Client{Timeout:30*time.Second}; resp,_:=client.Do(req); if resp!=nil{defer resp.Body.Close()}}
func countCached()int{c:=0; db.RLock(); for _,s:=range db.Songs{if s.TgFileID!=""{c++}}; db.RUnlock(); return c}
func telegramDownloadDB() error{if cfg.TelegramToken==""||cfg.TelegramFileID==""{return fmt.Errorf("no file_id")}; u:=fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s",cfg.TelegramToken,cfg.TelegramFileID); resp,err:=httpClient.Get(u); if err!=nil{return err}; defer resp.Body.Close(); var r struct{Ok bool; Result struct{FilePath string `json:"file_path"`} `json:"result"`}; json.NewDecoder(resp.Body).Decode(&r); if r.Result.FilePath==""{return fmt.Errorf("empty path")}; down:=fmt.Sprintf("https://api.telegram.org/file/bot%s/%s",cfg.TelegramToken,r.Result.FilePath); resp2,_:=httpClient.Get(down); defer resp2.Body.Close(); b,_:=io.ReadAll(resp2.Body); return os.WriteFile(cfg.DbPath,b,0644)}
func telegramGetFileUrl(fileID string)(string,error){if cfg.TelegramToken==""{return "",fmt.Errorf("no token")}; u:=fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s",cfg.TelegramToken,fileID); resp,err:=httpClient.Get(u); if err!=nil{return "",err}; defer resp.Body.Close(); var r struct{Ok bool; Result struct{FilePath string `json:"file_path"`} `json:"result"`}; if err:=json.NewDecoder(resp.Body).Decode(&r); err!=nil{return "",err}; if r.Result.FilePath==""{return "",fmt.Errorf("empty file_path")}; return fmt.Sprintf("https://api.telegram.org/file/bot%s/%s",cfg.TelegramToken,r.Result.FilePath),nil}
func telegramUploadAudioFile(filePathOrUrl string, song *Song)(string,string,error){
	if cfg.TelegramToken==""||cfg.TelegramMusicChatID==""{return "","",fmt.Errorf("tg not configured")}
	filename:=fmt.Sprintf("%s - %s.m4a",song.Artist,song.Title); filename=strings.ReplaceAll(filename,"/","_"); filename=strings.ReplaceAll(filename,"\"","")
	var data []byte
	if strings.HasPrefix(filePathOrUrl,"http"){resp,err:=httpClient.Get(filePathOrUrl); if err!=nil{return "","",err}; defer resp.Body.Close(); data,_=io.ReadAll(io.LimitReader(resp.Body,48*1024*1024))} else {f,err:=os.Open(filePathOrUrl); if err!=nil{return "","",err}; defer f.Close(); data,_=io.ReadAll(io.LimitReader(f,48*1024*1024))}
	body:=&bytes.Buffer{}; w:=multipart.NewWriter(body); w.WriteField("chat_id",cfg.TelegramMusicChatID); w.WriteField("caption",fmt.Sprintf("🎵 %s - %s | %s",song.Artist,song.Title,song.ID)); part,_:=w.CreateFormFile("audio",filename); part.Write(data); w.Close()
	apiUrl:=fmt.Sprintf("https://api.telegram.org/bot%s/sendAudio",cfg.TelegramToken); req,_:=http.NewRequest("POST",apiUrl,body); req.Header.Set("Content-Type",w.FormDataContentType()); client:=&http.Client{Timeout:60*time.Second}; resp,err:=client.Do(req); if err!=nil{return "","",err}; defer resp.Body.Close(); b,_:=io.ReadAll(resp.Body)
	var res struct{Ok bool; Result struct{Audio struct{FileID string `json:"file_id"`; FileUniqueID string `json:"file_unique_id"`} `json:"audio"`; Document struct{FileID string `json:"file_id"`; FileUniqueID string `json:"file_unique_id"`} `json:"document"`} `json:"result"`; Description string `json:"description"`}; json.Unmarshal(b,&res)
	if !res.Ok{body2:=&bytes.Buffer{}; w2:=multipart.NewWriter(body2); w2.WriteField("chat_id",cfg.TelegramMusicChatID); w2.WriteField("caption",fmt.Sprintf("🎵 %s - %s | %s",song.Artist,song.Title,song.ID)); part2,_:=w2.CreateFormFile("document",filename); part2.Write(data); w2.Close(); apiUrl2:=fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument",cfg.TelegramToken); req2,_:=http.NewRequest("POST",apiUrl2,body2); req2.Header.Set("Content-Type",w2.FormDataContentType()); resp2,err2:=client.Do(req2); if err2!=nil{return "","",fmt.Errorf("audio fail: %s doc err: %v",res.Description,err2)}; defer resp2.Body.Close(); b2,_:=io.ReadAll(resp2.Body); var res2 struct{Ok bool; Result struct{Document struct{FileID string `json:"file_id"`; FileUniqueID string `json:"file_unique_id"`} `json:"document"`} `json:"result"`; Description string `json:"description"`}; json.Unmarshal(b2,&res2); if !res2.Ok{return "","",fmt.Errorf("tg upload failed audio:%s doc:%s",res.Description,res2.Description)}; return res2.Result.Document.FileID,res2.Result.Document.FileUniqueID,nil}
	fid:=res.Result.Audio.FileID; if fid==""{fid=res.Result.Document.FileID}; fuid:=res.Result.Audio.FileUniqueID; if fuid==""{fuid=res.Result.Document.FileUniqueID}; if fid==""{return "","",fmt.Errorf("no file_id")}; return fid,fuid,nil
}
func cacheSongToTelegram(s *Song, streamUrl string){if s.TgFileID!=""{return}; if _,inProg:=tgCacheInProgress.Load(s.ID); inProg{return}; tgCacheInProgress.Store(s.ID,true); defer tgCacheInProgress.Delete(s.ID); fid,fuid,err:=telegramUploadAudioFile(streamUrl,s); if err!=nil{log.Printf("TG cache failed %s: %v",s.ID,err); return}; db.Lock(); if song,ok:=db.Songs[s.ID]; ok{song.TgFileID=fid; song.TgFileUniqueID=fuid; song.CachedAt=time.Now().Format(time.RFC3339)}; db.Unlock(); db.save(); go telegramUploadDB()}
func checkAuth(r *http.Request)(bool,string){
	q:=r.URL.Query(); u:=q.Get("u"); if u==""{u=q.Get("username")}; p:=q.Get("p"); t:=q.Get("t"); s:=q.Get("s")
	if u==""{if strings.Contains(r.URL.Path,"ping"){return true,cfg.SubUser}; muUsers.RLock(); if len(usersMap)==1{for user:=range usersMap{muUsers.RUnlock(); return true,user}}; muUsers.RUnlock(); return false,""}
	muUsers.RLock(); exp,ok:=usersMap[u]; muUsers.RUnlock(); if !ok{return false,""}
	if t!=""&&s!=""{h:=md5.New(); h.Write([]byte(exp+s)); if fmt.Sprintf("%x",h.Sum(nil))==t{return true,u}; return false,""}
	if p!=""{if strings.HasPrefix(p,"enc:"){if d,err:=hex.DecodeString(p[4:]); err==nil{p=string(d)}}; if p==exp{return true,u}}
	return false,""
}
func writeJSON(w http.ResponseWriter,s int,p interface{}){w.Header().Set("Content-Type","application/json"); w.Header().Set("Access-Control-Allow-Origin","*"); w.WriteHeader(s); json.NewEncoder(w).Encode(p)}
func subOK(d map[string]interface{})map[string]interface{}{b:=map[string]interface{}{"status":"ok","version":"1.16.1","type":"go-koyeb-env-ui","serverVersion":"4.1-koyeb-env","openSubsonic":true}; for k,v:=range d{b[k]=v}; return map[string]interface{}{"subsonic-response":b}}
func subFail(m string,c int)map[string]interface{}{return map[string]interface{}{"subsonic-response":map[string]interface{}{"status":"failed","version":"1.16.1","error":map[string]interface{}{"code":c,"message":m}}}}
func respond(w http.ResponseWriter,r *http.Request,d map[string]interface{}){w.Header().Set("Access-Control-Allow-Origin","*"); w.Header().Set("Access-Control-Allow-Methods","GET, POST, OPTIONS"); w.Header().Set("Access-Control-Allow-Headers","*"); writeJSON(w,200,subOK(d))}
func corsMiddleware(next http.Handler) http.Handler{
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){
		w.Header().Set("Access-Control-Allow-Origin","*")
		w.Header().Set("Access-Control-Allow-Methods","GET, POST, OPTIONS, DELETE")
		w.Header().Set("Access-Control-Allow-Headers","*")
		if r.Method=="OPTIONS"{w.WriteHeader(204); return}
		next.ServeHTTP(w,r)
	})
}
func slugArtist(n string)string{return "ar_"+url.PathEscape(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(n)," ","_")))}
func slugAlbum(n,a string)string{return "al_"+url.PathEscape(strings.ToLower(strings.ReplaceAll(a+"_"+n," ","_")))}
func ensureArtist(n string)*Artist{if n==""{n="Unknown Artist"}; id:=slugArtist(n); db.RLock(); if a,ok:=db.Artists[id]; ok{db.RUnlock(); return a}; db.RUnlock(); a:=&Artist{ID:id,Name:n}; db.Lock(); db.Artists[id]=a; db.Unlock(); return a}
func ensureAlbum(name,artist,artistId,thumb string)*Album{if name==""{name="Unknown Album"}; id:=slugAlbum(name,artist); db.RLock(); if al,ok:=db.Albums[id]; ok{db.RUnlock(); return al}; db.RUnlock(); al:=&Album{ID:id,Name:name,Artist:artist,ArtistID:artistId,CoverArt:id,ThumbURL:thumb,SongIDs:[]string{}}; db.Lock(); db.Albums[id]=al; db.Unlock(); return al}
func contains(arr []string,s string)bool{for _,v:=range arr{if v==s{return true}}; return false}
func parseJioImage(img interface{})string{switch v:=img.(type){case string:return v; case []interface{}:if len(v)>0{if m,ok:=v[len(v)-1].(map[string]interface{}); ok{if u,ok:=m["url"].(string); ok{return u}}}}; return ""}
func parseJioDownloadUrl(d interface{})string{if s,ok:=d.(string); ok{return s}; if arr,ok:=d.([]interface{}); ok{var best string; for _,it:=range arr{if m,ok:=it.(map[string]interface{}); ok{link,_:=m["link"].(string); if link==""{link,_=m["url"].(string)}; if q,_:=m["quality"].(string); strings.Contains(q,"320"){return link}; best=link}}; return best}; return ""}
func searchJio(query string)([]*Song,error){
	if query==""{return nil,nil}; ep:=fmt.Sprintf("%s/search?query=%s",cfg.JioApiUrl,url.QueryEscape(query)); resp,err:=httpClient.Get(ep); if err!=nil{return nil,err}; defer resp.Body.Close()
	body,_:=io.ReadAll(resp.Body); var g map[string]interface{}; json.Unmarshal(body,&g); var raw []interface{}
	if data,ok:=g["data"].(map[string]interface{}); ok{if res,ok:=data["results"].([]interface{}); ok{raw=res}}
	var songs []*Song
	for _,rs:=range raw{m,_:=rs.(map[string]interface{}); title,_:=m["name"].(string); if title==""{continue}; artist,_:=m["primaryArtists"].(string); if artist==""{artist="Various"}; id,_:=m["id"].(string); thumb:=parseJioImage(m["image"]); artistObj:=ensureArtist(artist); albumObj:=ensureAlbum(title+" Single",artist,artistObj.ID,thumb); s:=&Song{ID:"jio_"+id,Title:title,Artist:artist,ArtistID:artistObj.ID,Album:albumObj.Name,AlbumID:albumObj.ID,CoverArt:albumObj.ID,Source:"jio",SourceID:id,ThumbURL:thumb}; if dl,ok:=m["downloadUrl"]; ok{s.StreamURL=parseJioDownloadUrl(dl)}; songs=append(songs,s); db.Lock(); db.Songs[s.ID]=s; if !contains(albumObj.SongIDs,s.ID){albumObj.SongIDs=append(albumObj.SongIDs,s.ID)}; db.Unlock()}
	if len(songs)>0{go func(){db.save(); go telegramUploadDB()}()}; return songs,nil
}
func getJioStream(jioId string)(string,error){
	if v,ok:=streamCache.Load("jio_"+jioId); ok{c:=v.(cachedURL); if time.Now().Before(c.Expiry){return c.URL,nil}}
	ep:=fmt.Sprintf("%s/songs?id=%s",cfg.JioApiUrl,jioId); resp,_:=httpClient.Get(ep)
	if resp!=nil{defer resp.Body.Close(); body,_:=io.ReadAll(resp.Body); var g map[string]interface{}; json.Unmarshal(body,&g); if data,ok:=g["data"].([]interface{}); ok{for _,it:=range data{if m,ok:=it.(map[string]interface{}); ok{if dl,ok:=m["downloadUrl"]; ok{u:=parseJioDownloadUrl(dl); if u!=""{streamCache.Store("jio_"+jioId,cachedURL{URL:u,Expiry:time.Now().Add(time.Hour)}); return u,nil}}}}}}
	return "",fmt.Errorf("jio not found")
}
type YTSearch struct{Items []struct{ID struct{VideoID string `json:"videoId"`} `json:"id"`; Snippet struct{Title string `json:"title"`; ChannelTitle string `json:"channelTitle"`; Thumbnails map[string]struct{URL string `json:"url"`} `json:"thumbnails"`} `json:"snippet"`} `json:"items"`}
func searchYoutube(q string)([]*Song,error){
	if cfg.YtApiKey==""{return nil,fmt.Errorf("no key")}; u:=fmt.Sprintf("https://www.googleapis.com/youtube/v3/search?part=snippet&type=video&videoCategoryId=10&maxResults=15&q=%s&key=%s",url.QueryEscape(q),cfg.YtApiKey)
	resp,_:=httpClient.Get(u); if resp==nil{return nil,fmt.Errorf("yt fail")}; defer resp.Body.Close()
	var yt YTSearch; json.NewDecoder(resp.Body).Decode(&yt); var songs []*Song
	for _,it:=range yt.Items{if it.ID.VideoID==""{continue}; thumb:=""; if t,ok:=it.Snippet.Thumbnails["high"]; ok{thumb=t.URL}; art:=ensureArtist(it.Snippet.ChannelTitle); al:=ensureAlbum(it.Snippet.ChannelTitle+" - YouTube",it.Snippet.ChannelTitle,art.ID,thumb); s:=&Song{ID:"yt_"+it.ID.VideoID,Title:it.Snippet.Title,Artist:it.Snippet.ChannelTitle,ArtistID:art.ID,Album:al.Name,AlbumID:al.ID,CoverArt:al.ID,Source:"yt",SourceID:it.ID.VideoID,ThumbURL:thumb}; songs=append(songs,s); db.Lock(); if existing,ok:=db.Songs[s.ID]; ok{if existing.TgFileID!=""{s.TgFileID=existing.TgFileID; s.TgFileUniqueID=existing.TgFileUniqueID; s.CachedAt=existing.CachedAt}}; db.Songs[s.ID]=s; if !contains(al.SongIDs,s.ID){al.SongIDs=append(al.SongIDs,s.ID)}; db.Unlock()}
	go func(){db.save(); go telegramUploadDB()}(); return songs,nil
}
func getYoutubeStream(vid string)(string,error){
	if v,ok:=streamCache.Load("yt_"+vid); ok{c:=v.(cachedURL); if time.Now().Before(c.Expiry){return c.URL,nil}}
	ytSem<-struct{}{}; defer func(){<-ytSem}()
	cookieContent:=getCookiesContent(); cookiePath:=""; if cookieContent!=""{cookiePath="/tmp/cookies.txt"; os.WriteFile(cookiePath,[]byte(cookieContent),0600)}
	args:=[]string{"--no-playlist","--get-url","-f","bestaudio[ext=m4a]/bestaudio","https://www.youtube.com/watch?v="+vid}
	if cookiePath!=""{args=append([]string{"--cookies",cookiePath},args...)}
	cmd:=exec.Command("yt-dlp",args...); var out bytes.Buffer; var errBuf bytes.Buffer; cmd.Stdout=&out; cmd.Stderr=&errBuf
	done:=make(chan error,1); go func(){done<-cmd.Run()}()
	select{case err:=<-done:if err!=nil{return "",fmt.Errorf("%v %s",err,errBuf.String())}; case <-time.After(20*time.Second):cmd.Process.Kill(); return "",fmt.Errorf("timeout")}
	for _,line:=range strings.Split(strings.TrimSpace(out.String()),"\n"){if strings.HasPrefix(strings.TrimSpace(line),"http"){streamCache.Store("yt_"+vid,cachedURL{URL:strings.TrimSpace(line),Expiry:time.Now().Add(30*time.Minute)}); return strings.TrimSpace(line),nil}}
	return "",fmt.Errorf("no url")
}
func extractPlaylistID(input string)string{if strings.HasPrefix(input,"PL")&&!strings.Contains(input,"http"){return input}; u,err:=url.Parse(input); if err==nil{if list:=u.Query().Get("list"); list!=""{return list}}; re:=regexp.MustCompile(`[?&]list=([a-zA-Z0-9_-]+)`); m:=re.FindStringSubmatch(input); if len(m)>1{return m[1]}; return input}
func importYoutubePlaylist(pid string,owner string)(*Playlist,error){
	if cfg.YtApiKey==""{return nil,fmt.Errorf("YT_API_KEY missing")}; pid=extractPlaylistID(pid); title:=pid
	infoURL:=fmt.Sprintf("https://www.googleapis.com/youtube/v3/playlists?part=snippet&id=%s&key=%s",pid,cfg.YtApiKey)
	if resp,err:=httpClient.Get(infoURL); err==nil{defer resp.Body.Close(); var pr struct{Items []struct{Snippet struct{Title string `json:"title"`} `json:"snippet"`} `json:"items"`}; json.NewDecoder(resp.Body).Decode(&pr); if len(pr.Items)>0{title=pr.Items[0].Snippet.Title}}
	var allIDs []string; pageToken:=""
	for{
		apiUrl:=fmt.Sprintf("https://www.googleapis.com/youtube/v3/playlistItems?part=snippet&maxResults=50&playlistId=%s&key=%s",pid,cfg.YtApiKey); if pageToken!=""{apiUrl+="&pageToken="+pageToken}
		resp,err:=httpClient.Get(apiUrl); if err!=nil{break}
		var pr struct{NextPageToken string `json:"nextPageToken"`; Items []struct{Snippet struct{ResourceID struct{VideoID string `json:"videoId"`} `json:"resourceId"`; Title string `json:"title"`; ChannelTitle string `json:"channelTitle"`; Thumbnails map[string]struct{URL string `json:"url"`} `json:"thumbnails"`} `json:"snippet"`} `json:"items"`}
		json.NewDecoder(resp.Body).Decode(&pr); resp.Body.Close()
		for _,it:=range pr.Items{if it.Snippet.ResourceID.VideoID==""{continue}; vid:=it.Snippet.ResourceID.VideoID; allIDs=append(allIDs,vid); thumb:=""; if t,ok:=it.Snippet.Thumbnails["high"]; ok{thumb=t.URL}; art:=ensureArtist(it.Snippet.ChannelTitle); al:=ensureAlbum(it.Snippet.ChannelTitle+" - YouTube",it.Snippet.ChannelTitle,art.ID,thumb); sID:="yt_"+vid; s:=&Song{ID:sID,Title:it.Snippet.Title,Artist:it.Snippet.ChannelTitle,ArtistID:art.ID,Album:al.Name,AlbumID:al.ID,CoverArt:al.ID,Source:"yt",SourceID:vid,ThumbURL:thumb}; db.Lock(); if existing,ok:=db.Songs[s.ID]; ok&&existing.TgFileID!=""{s.TgFileID=existing.TgFileID; s.TgFileUniqueID=existing.TgFileUniqueID; s.CachedAt=existing.CachedAt}; db.Songs[s.ID]=s; if !contains(al.SongIDs,s.ID){al.SongIDs=append(al.SongIDs,s.ID)}; db.Unlock()}
		if pr.NextPageToken==""{break}; pageToken=pr.NextPageToken; if len(allIDs)>500{break}
	}
	if len(allIDs)==0{return nil,fmt.Errorf("no videos")}; plID:="pl_"+pid
	pl:=&Playlist{ID:plID,Name:title,Owner:owner,Comment:fmt.Sprintf("YT Import %d songs",len(allIDs)),SongIDs:[]string{},Created:time.Now().Format(time.RFC3339),Source:"yt",SourceURL:"https://www.youtube.com/playlist?list="+pid}
	for _,vid:=range allIDs{pl.SongIDs=append(pl.SongIDs,"yt_"+vid)}; db.Lock(); db.Playlists[pl.ID]=pl; db.Unlock(); db.save(); go telegramUploadDB(); return pl,nil
}
func handlePing(w http.ResponseWriter,r *http.Request){respond(w,r,map[string]interface{}{})}
func handleLicense(w http.ResponseWriter,r *http.Request){respond(w,r,map[string]interface{}{"license":map[string]interface{}{"valid":true}})}
func handleMusicFolders(w http.ResponseWriter,r *http.Request){respond(w,r,map[string]interface{}{"musicFolders":map[string]interface{}{"musicFolder":[]map[string]interface{}{{"id":0,"name":"Music"}}}})}
func handleGetUser(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok{writeJSON(w,200,subFail("auth",40)); return}; respond(w,r,map[string]interface{}{"user":map[string]interface{}{"username":user,"adminRole":user==cfg.SubUser}})}
func handleGetArtists(w http.ResponseWriter,r *http.Request){db.RLock(); defer db.RUnlock(); idx:=map[string][]map[string]interface{}{}; for _,a:=range db.Artists{letter:="#"; if len(a.Name)>0{letter=strings.ToUpper(string(a.Name[0]))}; idx[letter]=append(idx[letter],map[string]interface{}{"id":a.ID,"name":a.Name})}; var indexes []map[string]interface{}; for k,v:=range idx{indexes=append(indexes,map[string]interface{}{"name":k,"artist":v})}; respond(w,r,map[string]interface{}{"artists":map[string]interface{}{"index":indexes}})}
func handleGetArtist(w http.ResponseWriter,r *http.Request){id:=r.URL.Query().Get("id"); db.RLock(); a,ok:=db.Artists[id]; if !ok{db.RUnlock(); writeJSON(w,200,subFail("not found",70)); return}; var albums []map[string]interface{}; for _,al:=range db.Albums{if al.ArtistID==id{albums=append(albums,map[string]interface{}{"id":al.ID,"name":al.Name,"songCount":len(al.SongIDs),"coverArt":al.ID})}}; db.RUnlock(); respond(w,r,map[string]interface{}{"artist":map[string]interface{}{"id":a.ID,"name":a.Name,"album":albums}})}
func handleGetAlbum(w http.ResponseWriter,r *http.Request){id:=r.URL.Query().Get("id"); db.RLock(); al,ok:=db.Albums[id]; if !ok{if s,ok2:=db.Songs[id]; ok2{al,ok=db.Albums[s.AlbumID]}}; if !ok{db.RUnlock(); writeJSON(w,200,subFail("not found",70)); return}; var songs []map[string]interface{}; for _,sid:=range al.SongIDs{if s,ok:=db.Songs[sid]; ok{songs=append(songs,songToSubsonic(s,r))}}; db.RUnlock(); respond(w,r,map[string]interface{}{"album":map[string]interface{}{"id":al.ID,"name":al.Name,"artist":al.Artist,"coverArt":al.ID,"song":songs,"songCount":len(songs)}})}
func handleGetSong(w http.ResponseWriter,r *http.Request){id:=r.URL.Query().Get("id"); db.RLock(); s,ok:=db.Songs[id]; db.RUnlock(); if !ok{writeJSON(w,200,subFail("not found",70)); return}; respond(w,r,map[string]interface{}{"song":songToSubsonic(s,r)})}
func songToSubsonic(s *Song,r *http.Request)map[string]interface{}{m:=map[string]interface{}{"id":s.ID,"title":s.Title,"artist":s.Artist,"album":s.Album,"albumId":s.AlbumID,"artistId":s.ArtistID,"coverArt":s.CoverArt,"duration":s.Duration,"contentType":"audio/mp4","suffix":"m4a"}; if s.TgFileID!=""{m["cached"]="tg"}; return m}
func handleSearch3(w http.ResponseWriter,r *http.Request){if ok,_:=checkAuth(r); !ok{writeJSON(w,200,subFail("auth",40)); return}; q:=r.URL.Query().Get("query"); sCount,_:=strconv.Atoi(r.URL.Query().Get("songCount")); if sCount==0{sCount=50}; var jio,yt []*Song; var wg sync.WaitGroup; wg.Add(2); go func(){defer wg.Done(); jio,_=searchJio(q)}(); go func(){defer wg.Done(); yt,_=searchYoutube(q)}(); wg.Wait(); all:=append(jio,yt...); if len(all)>sCount{all=all[:sCount]}; var res []map[string]interface{}; for _,s:=range all{res=append(res,songToSubsonic(s,r))}; respond(w,r,map[string]interface{}{"searchResult3":map[string]interface{}{"song":res}})}
func handleAlbumList2(w http.ResponseWriter,r *http.Request){db.RLock(); var albums []*Album; for _,al:=range db.Albums{albums=append(albums,al)}; db.RUnlock(); rand.Shuffle(len(albums),func(i,j int){albums[i],albums[j]=albums[j],albums[i]}); if len(albums)>20{albums=albums[:20]}; var res []map[string]interface{}; for _,al:=range albums{res=append(res,map[string]interface{}{"id":al.ID,"name":al.Name,"artist":al.Artist,"coverArt":al.ID,"songCount":len(al.SongIDs)})}; respond(w,r,map[string]interface{}{"albumList2":map[string]interface{}{"album":res}})}
func handleRandomSongs(w http.ResponseWriter,r *http.Request){db.RLock(); var all []*Song; for _,s:=range db.Songs{all=append(all,s)}; db.RUnlock(); rand.Shuffle(len(all),func(i,j int){all[i],all[j]=all[j],all[i]}); if len(all)>20{all=all[:20]}; var res []map[string]interface{}; for _,s:=range all{res=append(res,songToSubsonic(s,r))}; respond(w,r,map[string]interface{}{"randomSongs":map[string]interface{}{"song":res}})}
func handleStream(w http.ResponseWriter,r *http.Request){
	w.Header().Set("Access-Control-Allow-Origin","*")
	if ok,_:=checkAuth(r); !ok{writeJSON(w,200,subFail("auth",40)); return}
	id:=r.URL.Query().Get("id"); db.RLock(); s,ok:=db.Songs[id]; db.RUnlock(); if !ok{http.Error(w,"not found",404); return}
	db.Lock(); if _,ok:=db.Songs[id]; ok{db.Songs[id].PlayCount++}; db.Unlock()
	if s.TgFileID!=""{if tgUrl,err:=telegramGetFileUrl(s.TgFileID); err==nil{req,_:=http.NewRequest("GET",tgUrl,nil); resp,err:=httpClient.Do(req); if err==nil{defer resp.Body.Close(); w.Header().Set("Content-Type","audio/mp4"); w.Header().Set("X-Cache","TG"); w.Header().Set("Access-Control-Allow-Origin","*"); io.Copy(w,resp.Body); return}}}
	var urlStr string; var err error; if s.Source=="jio"{urlStr,_=getJioStream(s.SourceID); if urlStr==""{urlStr=s.StreamURL}}else{urlStr,err=getYoutubeStream(s.SourceID)}; if err!=nil||urlStr==""{http.Error(w,"stream fail",502); return}
	if s.TgFileID==""&&s.Source=="yt"{go cacheSongToTelegram(s,urlStr)}
	req,_:=http.NewRequest("GET",urlStr,nil); req.Header.Set("User-Agent","Mozilla/5.0"); resp,err:=httpClient.Do(req); if err!=nil{http.Error(w,"upstream",502); return}; defer resp.Body.Close(); w.Header().Set("Content-Type","audio/mp4"); w.Header().Set("Access-Control-Allow-Origin","*"); w.Header().Set("X-Cache","MISS"); io.Copy(w,resp.Body)
}
func handleDownload(w http.ResponseWriter,r *http.Request){
	w.Header().Set("Access-Control-Allow-Origin","*")
	if ok,_:=checkAuth(r); !ok{writeJSON(w,200,subFail("auth",40)); return}
	id:=r.URL.Query().Get("id"); db.RLock(); s,ok:=db.Songs[id]; db.RUnlock(); if !ok{http.Error(w,"not found",404); return}
	var urlStr string; if s.TgFileID!=""{if tgUrl,err:=telegramGetFileUrl(s.TgFileID); err==nil{urlStr=tgUrl}}
	if urlStr==""{if s.Source=="jio"{urlStr,_=getJioStream(s.SourceID); if urlStr==""{urlStr=s.StreamURL}}else{var err error; urlStr,err=getYoutubeStream(s.SourceID); if err!=nil||urlStr==""{http.Error(w,"stream fail",502); return}}}
	req,_:=http.NewRequest("GET",urlStr,nil); req.Header.Set("User-Agent","Mozilla/5.0"); resp,err:=httpClient.Do(req); if err!=nil{http.Error(w,"upstream",502); return}; defer resp.Body.Close()
	w.Header().Set("Content-Type","audio/mp4"); w.Header().Set("Access-Control-Allow-Origin","*"); w.Header().Set("Content-Disposition",fmt.Sprintf("attachment; filename=\"%s - %s.m4a\"",s.Artist,s.Title)); io.Copy(w,resp.Body)
}
func handleCoverArt(w http.ResponseWriter,r *http.Request){w.Header().Set("Access-Control-Allow-Origin","*"); id:=r.URL.Query().Get("id"); var thumb string; db.RLock(); if al,ok:=db.Albums[id]; ok{thumb=al.ThumbURL}else if s,ok:=db.Songs[id]; ok{thumb=s.ThumbURL}; db.RUnlock(); if thumb==""{http.Redirect(w,r,"https://via.placeholder.com/500x500.png?text=No+Art",302); return}; resp,_:=httpClient.Get(thumb); if resp==nil{http.Redirect(w,r,thumb,302); return}; defer resp.Body.Close(); w.Header().Set("Content-Type","image/jpeg"); w.Header().Set("Cache-Control","public, max-age=86400"); w.Header().Set("Access-Control-Allow-Origin","*"); io.Copy(w,resp.Body)}
func handleGetPlaylists(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok{writeJSON(w,200,subFail("auth",40)); return}; db.RLock(); defer db.RUnlock(); var list []map[string]interface{}; for _,pl:=range db.Playlists{if pl.Owner==user||pl.Public||user==cfg.SubUser{list=append(list,map[string]interface{}{"id":pl.ID,"name":pl.Name,"owner":pl.Owner,"songCount":len(pl.SongIDs),"created":pl.Created})}}; respond(w,r,map[string]interface{}{"playlists":map[string]interface{}{"playlist":list}})}
func handleGetPlaylist(w http.ResponseWriter,r *http.Request){if ok,_:=checkAuth(r); !ok{writeJSON(w,200,subFail("auth",40)); return}; id:=r.URL.Query().Get("id"); db.RLock(); pl,ok:=db.Playlists[id]; db.RUnlock(); if !ok{writeJSON(w,200,subFail("not found",70)); return}; db.RLock(); var songs []map[string]interface{}; for _,sid:=range pl.SongIDs{if s,ok:=db.Songs[sid]; ok{songs=append(songs,songToSubsonic(s,r))}}; db.RUnlock(); respond(w,r,map[string]interface{}{"playlist":map[string]interface{}{"id":pl.ID,"name":pl.Name,"owner":pl.Owner,"songCount":len(songs),"entry":songs,"created":pl.Created}})}
func handleCreatePlaylist(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok{writeJSON(w,200,subFail("auth",40)); return}; name:=r.URL.Query().Get("name"); if name==""{name="New Playlist"}; id:="pl_"+fmt.Sprintf("%d",time.Now().UnixNano()); pl:=&Playlist{ID:id,Name:name,Owner:user,SongIDs:[]string{},Created:time.Now().Format(time.RFC3339)}; db.Lock(); db.Playlists[id]=pl; db.Unlock(); db.save(); respond(w,r,map[string]interface{}{"playlist":map[string]interface{}{"id":pl.ID,"name":pl.Name}})}
func handleUpdatePlaylist(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok{writeJSON(w,200,subFail("auth",40)); return}; id:=r.URL.Query().Get("playlistId"); if id==""{id=r.URL.Query().Get("id")}; name:=r.URL.Query().Get("name"); toAdd:=r.URL.Query()["songIdToAdd"]; toRemoveIdxStr:=r.URL.Query()["songIndexToRemove"]; db.Lock(); pl,ok:=db.Playlists[id]; if !ok{db.Unlock(); writeJSON(w,200,subFail("not found",70)); return}; if pl.Owner!=user&&user!=cfg.SubUser{db.Unlock(); writeJSON(w,200,subFail("not owner",50)); return}; if name!=""{pl.Name=name}; for _,sid:=range toAdd{if _,exists:=db.Songs[sid]; exists{if !contains(pl.SongIDs,sid){pl.SongIDs=append(pl.SongIDs,sid)}}}; if len(toRemoveIdxStr)>0{var idxs []int; for _,s:=range toRemoveIdxStr{if i,err:=strconv.Atoi(s); err==nil{idxs=append(idxs,i)}}; for a:=0;a<len(idxs);a++{for b:=a+1;b<len(idxs);b++{if idxs[a]<idxs[b]{idxs[a],idxs[b]=idxs[b],idxs[a]}}}; for _,idx:=range idxs{if idx>=0&&idx<len(pl.SongIDs){pl.SongIDs=append(pl.SongIDs[:idx],pl.SongIDs[idx+1:]...)}}}; db.Unlock(); db.save(); go telegramUploadDB(); respond(w,r,map[string]interface{}{})}
func handleDeletePlaylist(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok{writeJSON(w,200,subFail("auth",40)); return}; id:=r.URL.Query().Get("id"); db.Lock(); if pl,ok:=db.Playlists[id]; ok{if pl.Owner==user||user==cfg.SubUser{delete(db.Playlists,id)}}; db.Unlock(); db.save(); respond(w,r,map[string]interface{}{})}
func handleImportYT(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok{writeJSON(w,200,subFail("auth",40)); return}; u:=r.URL.Query().Get("url"); if u==""{u=r.URL.Query().Get("id")}; if u==""{u=r.URL.Query().Get("playlistId")}; pl,err:=importYoutubePlaylist(u,user); if err!=nil{writeJSON(w,200,subFail(err.Error(),0)); return}; respond(w,r,map[string]interface{}{"playlist":map[string]interface{}{"id":pl.ID,"name":pl.Name,"songCount":len(pl.SongIDs)}})}
func handleCreateUser(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok{writeJSON(w,200,subFail("auth",40)); return}; if user!=cfg.SubUser{writeJSON(w,200,subFail("only admin",50)); return}; newU:=r.URL.Query().Get("username"); newP:=r.URL.Query().Get("password"); if newU==""||newP==""{writeJSON(w,200,subFail("username password required",10)); return}; muUsers.Lock(); usersMap[newU]=newP; muUsers.Unlock(); db.Lock(); db.Users[newU]=newP; db.Unlock(); db.save(); go telegramUploadDB(); respond(w,r,map[string]interface{}{"user":map[string]interface{}{"username":newU}})}
func handleGetUsers(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok||user!=cfg.SubUser{writeJSON(w,200,subFail("admin only",50)); return}; muUsers.RLock(); var list []map[string]interface{}; for u := range usersMap{list=append(list,map[string]interface{}{"username":u,"adminRole":u==cfg.SubUser})}; muUsers.RUnlock(); respond(w,r,map[string]interface{}{"users":map[string]interface{}{"user":list}})}
func handleDeleteUser(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok||user!=cfg.SubUser{writeJSON(w,200,subFail("admin only",50)); return}; delU:=r.URL.Query().Get("username"); if delU==cfg.SubUser{writeJSON(w,200,subFail("cannot delete admin",10)); return}; muUsers.Lock(); delete(usersMap,delU); muUsers.Unlock(); db.Lock(); delete(db.Users,delU); db.Unlock(); db.save(); respond(w,r,map[string]interface{}{})}
func handleStar(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok{writeJSON(w,200,subFail("auth",40)); return}; ids:=r.URL.Query()["id"]; db.Lock(); if _,ok:=db.Starred[user]; !ok{db.Starred[user]=make(map[string]bool)}; for _,id:=range ids{db.Starred[user][id]=true}; db.Unlock(); db.save(); respond(w,r,map[string]interface{}{})}
func handleUnstar(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok{writeJSON(w,200,subFail("auth",40)); return}; ids:=r.URL.Query()["id"]; db.Lock(); if m,ok:=db.Starred[user]; ok{for _,id:=range ids{delete(m,id)}}; db.Unlock(); db.save(); respond(w,r,map[string]interface{}{})}
func handleGetStarred(w http.ResponseWriter,r *http.Request){ok,user:=checkAuth(r); if !ok{writeJSON(w,200,subFail("auth",40)); return}; db.RLock(); starMap:=db.Starred[user]; var songs []map[string]interface{}; for sid := range starMap{if s,ok:=db.Songs[sid]; ok{songs=append(songs,songToSubsonic(s,r))}}; db.RUnlock(); respond(w,r,map[string]interface{}{"starred":map[string]interface{}{"song":songs}})}
func handleScrobble(w http.ResponseWriter,r *http.Request){id:=r.URL.Query().Get("id"); if id!=""{db.Lock(); if s,ok:=db.Songs[id]; ok{s.PlayCount++}; db.Unlock()}; respond(w,r,map[string]interface{}{})}
func handleScanStatus(w http.ResponseWriter,r *http.Request){respond(w,r,map[string]interface{}{"scanStatus":map[string]interface{}{"scanning":false,"count":len(db.Songs)}})}
func handleConnections(w http.ResponseWriter,r *http.Request){
	if ok,_:=checkAuth(r); !ok{writeJSON(w,200,subFail("auth",40)); return}
	status := checkConnections()
	db.RLock()
	songsCount := len(db.Songs)
	cachedCount := 0
	for _,s := range db.Songs { if s.TgFileID != "" { cachedCount++ } }
	db.RUnlock()
	respond(w,r,map[string]interface{}{
		"connections": status,
		"stats": map[string]interface{}{"songs": songsCount, "cached": cachedCount, "playlists": len(db.Playlists), "users": len(usersMap)},
		"env": map[string]interface{}{
			"jioUrl": cfg.JioApiUrl,
			"hasYtKey": cfg.YtApiKey != "",
			"hasTgToken": cfg.TelegramToken != "",
			"hasCookies": getCookiesContent() != "",
			"cookiesSize": len(getCookiesContent()),
			"dbPath": cfg.DbPath,
			"adminUser": cfg.SubUser,
		},
	})
}
func handleTgIndex(w http.ResponseWriter,r *http.Request){
	if ok,_:=checkAuth(r); !ok{writeJSON(w,200,subFail("auth",40)); return}
	action := r.URL.Query().Get("action")
	if action == "delete" {
		id := r.URL.Query().Get("id")
		db.Lock()
		if s,ok := db.Songs[id]; ok {
			s.TgFileID = ""
			s.TgFileUniqueID = ""
			s.CachedAt = ""
		}
		db.Unlock()
		db.save()
		respond(w,r,map[string]interface{}{"deleted": id})
		return
	}
	if action == "edit" {
		id := r.URL.Query().Get("id")
		title := r.URL.Query().Get("title")
		artist := r.URL.Query().Get("artist")
		db.Lock()
		if s,ok := db.Songs[id]; ok {
			if title != "" { s.Title = title }
			if artist != "" { s.Artist = artist }
		}
		db.Unlock()
		db.save()
		respond(w,r,map[string]interface{}{"edited": id})
		return
	}
	db.RLock()
	var list []map[string]interface{}
	for _,s := range db.Songs {
		list = append(list, map[string]interface{}{
			"id": s.ID, "title": s.Title, "artist": s.Artist, "source": s.Source, "sourceId": s.SourceID,
			"tgFileId": s.TgFileID, "cached": s.TgFileID != "", "cachedAt": s.CachedAt,
			"thumb": s.ThumbURL, "playCount": s.PlayCount,
		})
	}
	db.RUnlock()
	respond(w,r,map[string]interface{}{"tgIndex": list, "total": len(list), "cached": countCached()})
}
func handleEndpoints(w http.ResponseWriter,r *http.Request){
	if ok,_:=checkAuth(r); !ok{writeJSON(w,200,subFail("auth",40)); return}
	endpoints := []map[string]interface{}{
		{"path": "/rest/ping.view", "method": "GET", "desc": "Ping check auth"},
		{"path": "/rest/getPlaylists.view", "method": "GET", "desc": "List self playlists + admin all"},
		{"path": "/rest/getPlaylist.view?id=xxx", "method": "GET", "desc": "View playlist songs"},
		{"path": "/rest/createPlaylist.view?name=xxx", "method": "GET", "desc": "Create playlist"},
		{"path": "/rest/updatePlaylist.view?playlistId=xxx&songIdToAdd=xxx", "method": "GET", "desc": "Add/remove songs"},
		{"path": "/rest/deletePlaylist.view?id=xxx", "method": "GET", "desc": "Delete playlist"},
		{"path": "/rest/importYoutubePlaylist.view?url=PL...", "method": "GET", "desc": "Import YT playlist -> TG cache"},
		{"path": "/rest/search3.view?query=arijit", "method": "GET", "desc": "Search Jio + YT"},
		{"path": "/rest/stream.view?id=yt_xxx", "method": "GET", "desc": "Stream - TG cache first, else yt-dlp"},
		{"path": "/rest/download.view?id=yt_xxx", "method": "GET", "desc": "Offline download"},
		{"path": "/rest/getCoverArt.view?id=xxx", "method": "GET", "desc": "Album art proxy"},
		{"path": "/rest/star.view?id=xxx", "method": "GET", "desc": "Favourite add"},
		{"path": "/rest/unstar.view?id=xxx", "method": "GET", "desc": "Favourite remove"},
		{"path": "/rest/getStarred.view", "method": "GET", "desc": "List favourites"},
		{"path": "/rest/getArtists.view", "method": "GET", "desc": "Artists index"},
		{"path": "/rest/getAlbum.view?id=xxx", "method": "GET", "desc": "Album details"},
		{"path": "/rest/createUser.view?username=&password=", "method": "GET", "desc": "Admin create user"},
		{"path": "/rest/getUsers.view", "method": "GET", "desc": "Admin list users"},
		{"path": "/rest/deleteUser.view?username=xxx", "method": "GET", "desc": "Admin delete user"},
		{"path": "/rest/getConnections.view", "method": "GET", "desc": "TG/YT/Jio/cookies status - KOYEB ENV"},
		{"path": "/rest/tgIndex.view", "method": "GET", "desc": "List all cached + edit/delete - KOYEB ENV"},
		{"path": "/rest/getEndpoints.view", "method": "GET", "desc": "This list"},
		{"path": "/health", "method": "GET", "desc": "Health + stats + connections"},
	}
	respond(w,r,map[string]interface{}{"endpoints": endpoints})
}

const adminHTML = `<!DOCTYPE html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1"><title>MUSIC PRO v4.1 - Koyeb ENV</title>
<script src="https://cdn.tailwindcss.com"></script>
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.0/css/all.min.css">
<style>.mono{font-family:ui-monospace,Menlo,monospace}.tab-active{background:white;color:black !important}::-webkit-scrollbar{width:4px}::-webkit-scrollbar-thumb{background:#333;border-radius:10px}</style></head>
<body class="bg-black text-white min-h-screen">
<div id="loginLock" class="fixed inset-0 z-[100] bg-black flex items-center justify-center p-4">
<div class="bg-zinc-900 border border-zinc-800 rounded-[24px] p-8 w-full max-w-sm">
<div class="w-12 h-12 bg-white rounded-full flex items-center justify-center text-black mx-auto"><i class="fa-solid fa-music"></i></div>
<div class="text-center mt-4"><div class="font-bold text-lg">MUSIC PRO</div><div class="text-xs text-zinc-500 mono mt-1">Koyeb ENV UI • v4.1 Single Source</div></div>
<div class="mt-6 space-y-3">
<input id="loginU" placeholder="Username" value="admin" class="w-full bg-black border border-zinc-800 rounded-full px-4 py-3 text-sm outline-none">
<input id="loginP" placeholder="Password" type="password" value="admin" class="w-full bg-black border border-zinc-800 rounded-full px-4 py-3 text-sm outline-none">
<button onclick="doLogin()" class="w-full bg-white text-black rounded-full py-3 font-bold text-sm">Unlock - Use Koyeb ENV</button>
<div id="loginErr" class="text-xs text-red-400 mono text-center"></div>
<div class="text-[11px] text-zinc-600 mono text-center">Uses Koyeb ENV: SUBSONIC_USER/PASS<br>No separate Worker ENV • Single source</div>
</div>
</div>
</div>
<div class="sticky top-0 z-50 bg-black/80 backdrop-blur-xl border-b border-zinc-800">
<div class="max-w-6xl mx-auto px-3 py-3 flex items-center justify-between">
<div class="flex items-center gap-3"><div class="w-8 h-8 bg-white rounded-full flex items-center justify-center text-black"><i class="fa-solid fa-music text-xs"></i></div><div><div class="font-bold text-sm">MUSIC PRO</div><div class="text-[10px] text-zinc-500 mono">v4.1 KOYEB ENV • <span id="apiStatus" class="text-lime-400">online</span> • <span class="text-[9px] bg-lime-400 text-black px-1.5 py-0.5 rounded-full">SINGLE ENV</span></div></div></div>
<div class="flex items-center gap-2"><div class="text-[10px] mono bg-zinc-900 border border-zinc-800 px-2 py-1 rounded-full" id="userBadge">admin</div><button onclick="logout()" class="w-7 h-7 bg-zinc-900 border border-zinc-800 rounded-full text-xs"><i class="fa-solid fa-right-from-bracket"></i></button></div>
</div>
<div class="max-w-6xl mx-auto px-3 pb-2 flex gap-1.5 overflow-auto">
<button onclick="switchTab('dash')" data-tab="dash" class="tab-btn tab-active whitespace-nowrap px-4 py-2 rounded-full text-xs font-bold bg-zinc-900 border border-zinc-800"><i class="fa-solid fa-chart-simple mr-1"></i>Dashboard</button>
<button onclick="switchTab('player')" data-tab="player" class="tab-btn whitespace-nowrap px-4 py-2 rounded-full text-xs font-bold bg-zinc-900 border border-zinc-800 text-zinc-400"><i class="fa-solid fa-play mr-1"></i>Player</button>
<button onclick="switchTab('playlists')" data-tab="playlists" class="tab-btn whitespace-nowrap px-4 py-2 rounded-full text-xs font-bold bg-zinc-900 border border-zinc-800 text-zinc-400"><i class="fa-solid fa-list mr-1"></i>Playlists</button>
<button onclick="switchTab('tgindex')" data-tab="tgindex" class="tab-btn whitespace-nowrap px-4 py-2 rounded-full text-xs font-bold bg-zinc-900 border border-zinc-800 text-zinc-400"><i class="fa-brands fa-telegram mr-1"></i>TG Index</button>
<button onclick="switchTab('users')" data-tab="users" class="tab-btn whitespace-nowrap px-4 py-2 rounded-full text-xs font-bold bg-zinc-900 border border-zinc-800 text-zinc-400"><i class="fa-solid fa-users mr-1"></i>Users</button>
<button onclick="switchTab('connections')" data-tab="connections" class="tab-btn whitespace-nowrap px-4 py-2 rounded-full text-xs font-bold bg-zinc-900 border border-zinc-800 text-zinc-400"><i class="fa-solid fa-plug mr-1"></i>Connections</button>
<button onclick="switchTab('endpoints')" data-tab="endpoints" class="tab-btn whitespace-nowrap px-4 py-2 rounded-full text-xs font-bold bg-zinc-900 border border-zinc-800 text-zinc-400"><i class="fa-solid fa-code mr-1"></i>Endpoints</button>
</div>
</div>
<div class="max-w-6xl mx-auto p-3">
<div id="tab-dash" class="tab-pane">
<div class="grid grid-cols-2 md:grid-cols-4 gap-3">
<div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-4"><div class="text-[10px] text-zinc-500 mono">SONGS</div><div id="stSongs" class="text-xl font-bold">-</div><div class="text-[10px] text-zinc-600 mono">from Koyeb DB</div></div>
<div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-4"><div class="text-[10px] text-zinc-500 mono">TG CACHED</div><div id="stCached" class="text-xl font-bold text-cyan-400">-</div><div class="text-[10px] text-zinc-600 mono">Koyeb env TG</div></div>
<div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-4"><div class="text-[10px] text-zinc-500 mono">PLAYLISTS</div><div id="stPls" class="text-xl font-bold">-</div><div class="text-[10px] text-zinc-600 mono">Koyeb env users</div></div>
<div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-4"><div class="text-[10px] text-zinc-500 mono">USERS</div><div id="stUsers" class="text-xl font-bold">-</div><div class="text-[10px] text-zinc-600 mono">Koyeb SUBSONIC_USERS</div></div>
</div>
<div class="mt-4 bg-zinc-900 border border-zinc-800 rounded-2xl p-4">
<div class="font-bold text-sm"><i class="fa-solid fa-download mr-2"></i>Import YouTube Playlist (Uses Koyeb YT_API_KEY + YT_COOKIES_B64)</div>
<div class="flex gap-2 mt-3">
<input id="yturl" placeholder="https://www.youtube.com/playlist?list=PL..." class="flex-1 bg-black border border-zinc-800 rounded-full px-4 py-3 text-sm mono outline-none">
<button onclick="importYT()" class="bg-white text-black px-6 py-3 rounded-full text-sm font-bold">Import</button>
</div>
<div id="ytRes" class="mt-2 text-xs mono bg-black border border-zinc-800 rounded-xl p-3 max-h-32 overflow-auto hidden"></div>
<div class="mt-2 text-[10px] text-zinc-500 mono">API Base: <span id="apiBase" class="text-white"></span> (location.origin - Koyeb ENV single source)</div>
</div>
<div class="mt-4 grid md:grid-cols-3 gap-3" id="connCards"></div>
</div>
<div id="tab-player" class="tab-pane hidden">
<div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-4">
<div class="flex gap-4"><img id="pArt" src="https://via.placeholder.com/200" class="w-28 h-28 rounded-2xl object-cover bg-zinc-800"><div class="flex-1"><div id="pTitle" class="font-bold">No song</div><div id="pArtist" class="text-sm text-zinc-500">Import playlist</div><div class="mt-2 flex gap-2"><span id="pCache" class="text-[10px] px-2 py-1 bg-zinc-800 rounded-full mono">-</span><span id="pSource" class="text-[10px] px-2 py-1 bg-zinc-800 rounded-full mono">-</span></div><div class="mt-3 flex gap-2"><button onclick="prevSong()" class="bg-zinc-800 border border-zinc-700 px-4 py-2 rounded-full text-xs"><i class="fa-solid fa-backward"></i></button><button onclick="togglePlay()" id="playBtn" class="bg-white text-black px-6 py-2 rounded-full text-xs font-bold"><i class="fa-solid fa-play"></i></button><button onclick="nextSong()" class="bg-zinc-800 border border-zinc-700 px-4 py-2 rounded-full text-xs"><i class="fa-solid fa-forward"></i></button><a id="dlBtn" href="#" target="_blank" class="bg-zinc-800 border border-zinc-700 px-4 py-2 rounded-full text-xs"><i class="fa-solid fa-download"></i></a></div></div></div>
<audio id="audio" controls class="w-full mt-4 h-10 bg-black rounded-full"></audio>
<div class="mt-4"><div class="text-xs font-bold mono text-zinc-500">QUEUE (Koyeb ENV)</div><div id="queue" class="mt-2 space-y-1 max-h-96 overflow-auto"></div></div>
</div>
</div>
<div id="tab-playlists" class="tab-pane hidden"><div class="flex justify-between items-center"><div class="font-bold text-sm">Playlists - Koyeb ENV (Self + Admin)</div><button onclick="loadPlaylists()" class="text-xs bg-zinc-800 px-3 py-1 rounded-full">Refresh</button></div><div id="pls" class="mt-3 space-y-2"></div></div>
<div id="tab-tgindex" class="tab-pane hidden">
<div class="flex flex-wrap gap-2 justify-between items-center"><div><div class="font-bold text-sm">TG Index - Koyeb ENV TG Cache</div><div class="text-[11px] text-zinc-500 mono">Add/Edit/Delete - Uses TELEGRAM_BOT_TOKEN + CHAT_ID from Koyeb</div></div><div class="flex gap-2"><input id="tgSearch" placeholder="Search..." oninput="filterTg()" class="bg-zinc-900 border border-zinc-800 rounded-full px-3 py-2 text-xs mono"><select id="tgFilter" onchange="filterTg()" class="bg-zinc-900 border border-zinc-800 rounded-full px-3 py-2 text-xs"><option value="all">All</option><option value="cached">Cached</option><option value="uncached">Not cached</option></select><button onclick="loadTgIndex()" class="bg-zinc-800 px-3 py-2 rounded-full text-xs">Reload</button></div></div>
<div id="tgStats" class="mt-3 grid grid-cols-3 gap-2 text-xs mono"></div><div id="tgList" class="mt-3 space-y-2 max-h-[70vh] overflow-auto"></div>
</div>
<div id="tab-users" class="tab-pane hidden">
<div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-4"><div class="font-bold text-sm"><i class="fa-solid fa-user-shield mr-2"></i>Admin - Full User Section - Koyeb ENV SUBSONIC_USERS</div><div class="mt-3 flex gap-2"><input id="newU" placeholder="username" class="flex-1 bg-black border border-zinc-800 rounded-full px-3 py-2 text-sm"><input id="newP" placeholder="password" class="flex-1 bg-black border border-zinc-800 rounded-full px-3 py-2 text-sm"><button onclick="createUser()" class="bg-white text-black px-4 py-2 rounded-full text-xs font-bold">Create (Koyeb ENV)</button></div><div id="userRes" class="mt-2 text-xs mono"></div><div class="mt-4"><div class="text-xs font-bold mono text-zinc-500">ALL USERS - Koyeb ENV</div><div id="usersList" class="mt-2 space-y-2"></div></div></div>
</div>
<div id="tab-connections" class="tab-pane hidden"><div class="font-bold text-sm">Connections - Koyeb ENV Live Check</div><div class="text-[11px] text-zinc-500 mono">Uses Koyeb env: TELEGRAM_BOT_TOKEN, YT_API_KEY, YT_COOKIES_B64, JIOSAAVN_API_URL - Single source, no Worker separate env</div><div id="connDetails" class="mt-3 space-y-3"></div><button onclick="loadConnections(true)" class="mt-4 w-full bg-zinc-900 border border-zinc-800 py-3 rounded-full text-xs font-bold">Force Refresh - Check Koyeb ENV</button><div class="mt-4 bg-black border border-zinc-800 rounded-xl p-3 text-[11px] mono"><div class="text-zinc-500">Koyeb ENV (non-secret):</div><div id="envInfo" class="mt-1 text-zinc-300"></div></div></div>
<div id="tab-endpoints" class="tab-pane hidden"><div class="font-bold text-sm">All Endpoints - Koyeb ENV API</div><div id="epList" class="mt-3 space-y-2"></div><pre id="epRes" class="mt-4 bg-black border border-zinc-800 rounded-xl p-3 text-xs mono max-h-64 overflow-auto"></pre></div>
</div>
<script>
let curList=[]; let curIdx=0; const KOYEB=location.origin; let AUTH={u:localStorage.getItem('u')||'admin',p:localStorage.getItem('p')||'admin'}; let tgAll=[];
function api(path){return KOYEB+path+(path.includes('?')?'&':'?')+'u='+encodeURIComponent(AUTH.u)+'&p='+encodeURIComponent(AUTH.p)+'&v=1.16.1&c=web&f=json'}
function checkLogin(){let u=localStorage.getItem('u'); let p=localStorage.getItem('p'); if(u&&p){AUTH={u,p}; document.getElementById('loginLock').style.display='none'; document.getElementById('userBadge').textContent=u; init();} document.getElementById('apiBase').textContent=KOYEB}
async function doLogin(){let u=document.getElementById('loginU').value.trim(); let p=document.getElementById('loginP').value.trim(); if(!u||!p){document.getElementById('loginErr').textContent='Enter user/pass'; return} try{let res=await fetch(KOYEB+'/rest/ping.view?u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p)+'&v=1.16.1&c=web&f=json'); let j=await res.json(); if(j['subsonic-response']?.status==='ok'){localStorage.setItem('u',u); localStorage.setItem('p',p); AUTH={u,p}; document.getElementById('loginLock').style.display='none'; document.getElementById('userBadge').textContent=u; init();} else {document.getElementById('loginErr').textContent='Auth failed';}}catch(e){document.getElementById('loginErr').textContent='Error: '+e}}
function logout(){localStorage.removeItem('u');localStorage.removeItem('p');location.reload()}
function switchTab(id){document.querySelectorAll('.tab-pane').forEach(el=>el.classList.add('hidden')); document.getElementById('tab-'+id).classList.remove('hidden'); document.querySelectorAll('.tab-btn').forEach(b=>{b.classList.remove('tab-active'); b.classList.add('text-zinc-400')}); document.querySelector('[data-tab="'+id+'"]').classList.add('tab-active'); document.querySelector('[data-tab="'+id+'"]').classList.remove('text-zinc-400'); if(id==='tgindex') loadTgIndex(); if(id==='users') loadUsers(); if(id==='connections') loadConnections(); if(id==='endpoints') loadEndpoints(); if(id==='playlists') loadPlaylists();}
async function init(){loadStats(); loadPlaylists(); loadConnections(); loadTgIndex(); loadUsers(); loadEndpoints();}
async function loadStats(){try{let r=await fetch(KOYEB+'/health'); let j=await r.json(); document.getElementById('stSongs').textContent=j.songs||0; document.getElementById('stCached').textContent=j.cached||0; document.getElementById('stPls').textContent=j.playlists||0; document.getElementById('stUsers').textContent=j.users||0; document.getElementById('apiStatus').textContent=j.status==='ok'?'online':'offline';}catch{}}
async function loadConnections(){try{let r=await fetch(api('/rest/getConnections.view')); let j=await r.json(); let c=j['subsonic-response']?.connections; if(!c) return; let cardsHtml = \'<div class="bg-zinc-900 border \' + (c.tg.working?'border-green-900':'border-red-900') + ' rounded-2xl p-4"><div class="flex justify-between"><div class="text-xs font-bold"><i class="fa-brands fa-telegram mr-1"></i>TELEGRAM (Koyeb ENV)</div><div class="text-[10px] px-2 py-1 rounded-full \' + (c.tg.working?'bg-green-900 text-green-300':'bg-red-900 text-red-300') + '">\' + (c.tg.working?'WORKING':'FAILED') + '</div></div><div class="mt-2 text-[11px] mono">Connected: \' + (c.tg.connected?'Yes':'No') + ' • Bot: \' + (c.tg.botName||'-') + ' • Chat: \' + (c.tg.chatID?c.tg.chatID.slice(0,15)+'...':'-') + '</div><div class="mt-1 text-[10px] text-zinc-500">\' + (c.tg.error||'OK - Koyeb TELEGRAM_BOT_TOKEN') + '</div></div><div class="bg-zinc-900 border \' + (c.yt.working?'border-green-900':'border-red-900') + ' rounded-2xl p-4"><div class="flex justify-between"><div class="text-xs font-bold"><i class="fa-brands fa-youtube mr-1"></i>YOUTUBE (Koyeb ENV)</div><div class="text-[10px] px-2 py-1 rounded-full \' + (c.yt.working?'bg-green-900 text-green-300':'bg-red-900 text-red-300') + '">\' + (c.yt.working?'WORKING':'FAILED') + '</div></div><div class="mt-2 text-[11px] mono">Key: \' + (c.yt.hasKey?'Yes':'No') + ' • Cookies: \' + (c.yt.hasCookies?'Yes ('+c.cookies.size+' bytes)':'No') + ' • yt-dlp: \' + (c.yt.ytdlpVersion||'-') + '</div><div class="mt-1 text-[10px] text-zinc-500">\' + (c.yt.error||'OK - Koyeb YT_API_KEY + YT_COOKIES_B64') + '</div></div><div class="bg-zinc-900 border \' + (c.jio.working?'border-green-900':'border-red-900') + ' rounded-2xl p-4"><div class="flex justify-between"><div class="text-xs font-bold">JIOSAAVN (Koyeb ENV)</div><div class="text-[10px] px-2 py-1 rounded-full \' + (c.jio.working?'bg-green-900 text-green-300':'bg-red-900 text-red-300') + '">\' + (c.jio.working?'WORKING':'FAILED') + '</div></div><div class="mt-2 text-[11px] mono">Connected: \' + (c.jio.connected?'Yes':'No') + ' • URL: \' + (c.jio.apiUrl) + '</div><div class="mt-1 text-[10px] text-zinc-500">\' + (c.jio.error||'OK - Koyeb JIOSAAVN_API_URL') + '</div></div>\'; document.getElementById('connCards').innerHTML=cardsHtml; let detailsHtml = \'<div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-4"><div class="flex justify-between"><b class="text-xs">Telegram Bot - Koyeb ENV</b><span class="text-[10px] px-2 py-1 rounded-full \' + (c.tg.connected?'bg-zinc-800':'bg-red-900') + '">\' + (c.tg.connected?'CONNECTED (Koyeb TELEGRAM_BOT_TOKEN)':'NOT CONNECTED') + '</span></div><div class="mt-2 text-[11px] mono">Bot @\' + (c.tg.botName||'?') + ' • ChatID \' + (c.tg.chatID||'?') + ' • Status \' + (c.tg.working?'WORKING ✅':'FAILED ❌') + '<br>Error: \' + (c.tg.error||'none') + '<br>ENV: TELEGRAM_BOT_TOKEN=\' + (j['subsonic-response'].env.hasTgToken?'set':'not set') + ' • CHAT_ID=\' + (c.tg.chatID?'set':'not set') + '</div></div><div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-4"><div class="flex justify-between"><b class="text-xs">YouTube + Cookies - Koyeb ENV</b><span class="text-[10px] px-2 py-1 rounded-full \' + (c.yt.working?'bg-green-900':'bg-red-900') + '">\' + (c.yt.working?'WORKING':'FAILED') + '</span></div><div class="mt-2 text-[11px] mono">API Key: \' + (c.yt.hasKey?'present (Koyeb YT_API_KEY)':'missing') + ' • Cookies: \' + (c.yt.hasCookies?c.cookies.size+' bytes valid='+c.cookies.valid+' (Koyeb YT_COOKIES_B64)':'missing') + ' • yt-dlp: \' + (c.yt.ytdlpVersion) + '<br>Error: \' + (c.yt.error||'none') + '</div></div><div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-4"><div class="flex justify-between"><b class="text-xs">JioSaavn - Koyeb ENV</b><span class="text-[10px] px-2 py-1 rounded-full \' + (c.jio.working?'bg-green-900':'bg-red-900') + '">\' + (c.jio.working?'WORKING':'FAILED') + '</span></div><div class="mt-2 text-[11px] mono">URL: \' + (c.jio.apiUrl) + ' (Koyeb JIOSAAVN_API_URL)<br>Status: \' + (c.jio.working?'WORKING ✅':'FAILED ❌') + '<br>Error: \' + (c.jio.error||'none') + '</div></div>\'; document.getElementById('connDetails').innerHTML=detailsHtml; document.getElementById('envInfo').innerHTML='Koyeb ENV Single Source - No Worker separate ENV<br>Songs: '+j['subsonic-response'].stats.songs+' • Cached: '+j['subsonic-response'].stats.cached+' • YT Key: '+(j['subsonic-response'].env.hasYtKey?'✅':'❌')+' • TG Token: '+(j['subsonic-response'].env.hasTgToken?'✅':'❌')+' • Cookies: '+(j['subsonic-response'].env.hasCookies?'✅ '+j['subsonic-response'].env.cookiesSize+' bytes':'❌')+' • Jio URL: '+j['subsonic-response'].env.jioUrl;}catch(e){document.getElementById('connDetails').textContent='Error: '+e}}
async function importYT(){let url=document.getElementById('yturl').value.trim(); if(!url) return alert('Paste YT URL'); let el=document.getElementById('ytRes'); el.classList.remove('hidden'); el.textContent='Importing using Koyeb ENV YT_API_KEY...'; try{let res=await fetch(api('/rest/importYoutubePlaylist.view')+'&url='+encodeURIComponent(url));let j=await res.json();el.textContent=JSON.stringify(j,null,2);loadStats();loadPlaylists();loadTgIndex();}catch(e){el.textContent='Error:'+e}}
async function loadPlaylists(){try{let r=await fetch(api('/rest/getPlaylists.view')); let j=await r.json(); let list=j['subsonic-response']?.playlists?.playlist||[]; if(!Array.isArray(list)) list=[list]; let html=list.map(pl=>'<div class="bg-zinc-900 border border-zinc-800 rounded-xl p-3 flex justify-between"><div><div class="font-bold text-sm">'+pl.name+'</div><div class="text-[10px] mono text-zinc-500">'+pl.id+' • '+(pl.owner||'')+' • '+pl.songCount+' songs (Koyeb ENV)</div></div><div class="flex gap-1"><button onclick="openPl(\\''+pl.id+'\\')" class="bg-white text-black px-3 py-1 rounded-full text-xs font-bold">Open</button><button onclick="deletePl(\\''+pl.id+'\\')" class="bg-zinc-800 px-2 py-1 rounded-full text-xs">Del</button></div></div>').join(''); document.getElementById('pls').innerHTML=html||'No playlists (Koyeb ENV)';}catch(e){document.getElementById('pls').textContent='Error:'+e}}
async function openPl(id){try{let r=await fetch(api('/rest/getPlaylist.view')+'&id='+id); let j=await r.json(); let pl=j['subsonic-response']?.playlist; let songs=pl?.entry||[]; if(!Array.isArray(songs)) songs=[songs]; curList=songs; curIdx=0; let qhtml=songs.map((s,i)=>'<div onclick="playIdx('+i+')" class="flex gap-2 p-2 hover:bg-zinc-800 rounded-xl cursor-pointer"><img src="'+KOYEB+'/rest/getCoverArt.view?id='+s.id+'&u='+AUTH.u+'&p='+AUTH.p+'" class="w-10 h-10 rounded-lg object-cover"><div class="flex-1"><div class="text-xs font-bold">'+s.title+'</div><div class="text-[10px] text-zinc-500">'+s.artist+(s.cached?' • TG Koyeb':'')+'</div></div></div>').join(''); document.getElementById('queue').innerHTML=qhtml; if(songs.length>0){switchTab('player'); playIdx(0);}}catch(e){alert(e)}}
async function deletePl(id){ if(!confirm('Delete?')) return; await fetch(api('/rest/deletePlaylist.view')+'&id='+id); loadPlaylists(); }
function playIdx(i){ if(!curList[i]) return; curIdx=i; let s=curList[i]; document.getElementById('pTitle').textContent=s.title; document.getElementById('pArtist').textContent=s.artist; document.getElementById('pArt').src=KOYEB+'/rest/getCoverArt.view?id='+s.id+'&u='+AUTH.u+'&p='+AUTH.p; document.getElementById('pCache').textContent=s.cached?'TG CACHED (Koyeb)':'MISS'; document.getElementById('pSource').textContent=s.id.split('_')[0]; let streamUrl=KOYEB+'/rest/stream.view?id='+s.id+'&u='+AUTH.u+'&p='+AUTH.p+'&v=1.16.1&c=web'; document.getElementById('audio').src=streamUrl; document.getElementById('audio').play(); document.getElementById('dlBtn').href=KOYEB+'/rest/download.view?id='+s.id+'&u='+AUTH.u+'&p='+AUTH.p+'&v=1.16.1&c=web';}
function nextSong(){ if(curIdx+1<curList.length) playIdx(curIdx+1) } function prevSong(){ if(curIdx>0) playIdx(curIdx-1) } function togglePlay(){let a=document.getElementById('audio'); if(a.paused) a.play(); else a.pause();}
async function loadTgIndex(){try{let r=await fetch(api('/rest/tgIndex.view')); let j=await r.json(); tgAll=j['subsonic-response']?.tgIndex||[]; document.getElementById('tgStats').innerHTML='<div class="bg-zinc-900 border border-zinc-800 rounded-full px-3 py-2">Total: '+tgAll.length+' (Koyeb DB)</div><div class="bg-green-900/30 border border-green-800 rounded-full px-3 py-2">Cached: '+tgAll.filter(s=>s.cached).length+' (Koyeb TG)</div><div class="bg-zinc-900 border border-zinc-800 rounded-full px-3 py-2">Not cached: '+tgAll.filter(s=>!s.cached).length+'</div>'; renderTg(tgAll);}catch(e){document.getElementById('tgList').textContent='Error:'+e}}
function renderTg(list){let html=list.map(s=>'<div class="bg-zinc-900 border border-zinc-800 rounded-xl p-3 flex gap-3"><img src="'+(s.thumb||'https://via.placeholder.com/40')+'" class="w-10 h-10 rounded-lg object-cover"><div class="flex-1"><div class="text-xs font-bold">'+s.title+'</div><div class="text-[10px] text-zinc-500 mono">'+s.artist+' • '+s.id+' • '+(s.cached?'TG '+ (s.cachedAt||'')+' (Koyeb)':'NOT CACHED')+'</div><div class="mt-1 flex gap-1"><button onclick="editSong(\\''+s.id+'\\')" class="bg-zinc-800 px-2 py-1 rounded-full text-[10px]">Edit (Koyeb DB)</button><button onclick="deleteCache(\\''+s.id+'\\')" class="bg-red-900/30 px-2 py-1 rounded-full text-[10px]">Del Cache</button><button onclick="playTg(\\''+s.id+'\\')" class="bg-white text-black px-2 py-1 rounded-full text-[10px]">Play</button></div></div><div class="text-[10px]"><span class="px-2 py-1 rounded-full '+(s.cached?'bg-green-900 text-green-300':'bg-zinc-800')+'">'+(s.cached?'CACHED':'MISS')+'</span></div></div>').join(''); document.getElementById('tgList').innerHTML=html||'No songs (Koyeb DB)';}
function filterTg(){let q=document.getElementById('tgSearch').value.toLowerCase(); let f=document.getElementById('tgFilter').value; let filtered=tgAll.filter(s=>{let matchQ = !q || s.title.toLowerCase().includes(q) || s.artist.toLowerCase().includes(q) || s.id.toLowerCase().includes(q); let matchF = f==='all' || (f==='cached' && s.cached) || (f==='uncached' && !s.cached); return matchQ && matchF;}); renderTg(filtered);}
async function deleteCache(id){ if(!confirm('Delete TG cache for '+id+'? Koyeb ENV')) return; await fetch(api('/rest/tgIndex.view')+'&action=delete&id='+id); loadTgIndex(); loadStats(); }
async function editSong(id){let t=prompt('New title (Koyeb DB edit):'); let a=prompt('New artist:'); if(t===null && a===null) return; let url=api('/rest/tgIndex.view')+'&action=edit&id='+id; if(t) url+='&title='+encodeURIComponent(t); if(a) url+='&artist='+encodeURIComponent(a); await fetch(url); loadTgIndex();}
function playTg(id){let s=tgAll.find(x=>x.id===id); if(!s) return; curList=tgAll; curIdx=tgAll.indexOf(s); switchTab('player'); playIdx(curIdx);}
async function loadUsers(){try{let r=await fetch(api('/rest/getUsers.view')); let j=await r.json(); let users=j['subsonic-response']?.users?.user||[]; if(!Array.isArray(users)) users=[users]; let html=users.map(u=>'<div class="bg-zinc-900 border border-zinc-800 rounded-xl p-3 flex justify-between"><div><div class="text-sm font-bold">'+u.username+(u.adminRole?' <span class=text-[10px] bg-white text-black px-2 py-0.5 rounded-full>ADMIN (Koyeb ENV)</span>':'')+'</div><div class="text-[10px] mono text-zinc-500">'+(u.adminRole?'full access - Koyeb SUBSONIC_USER':'self playlists only - Koyeb SUBSONIC_USERS')+'</div></div><div class="flex gap-1">'+(u.username!=='admin'?'<button onclick="delUser(\\''+u.username+'\\')" class="bg-red-900/30 px-3 py-1 rounded-full text-xs">Del (Koyeb ENV)</button>':'')+'</div></div>').join(''); document.getElementById('usersList').innerHTML=html;}catch(e){document.getElementById('usersList').textContent='Error: '+e+' (admin only - Koyeb ENV)'}}
async function createUser(){let u=document.getElementById('newU').value.trim(); let p=document.getElementById('newP').value.trim(); if(!u||!p) return alert('Enter user/pass - will save to Koyeb ENV DB'); let r=await fetch(api('/rest/createUser.view')+'&username='+encodeURIComponent(u)+'&password='+encodeURIComponent(p)); let j=await r.json(); document.getElementById('userRes').textContent=JSON.stringify(j,null,2)+' (Koyeb ENV)'; loadUsers(); loadStats();}
async function delUser(u){ if(!confirm('Delete user '+u+'? Koyeb ENV')) return; await fetch(api('/rest/deleteUser.view')+'&username='+encodeURIComponent(u)); loadUsers(); }
async function loadEndpoints(){try{let r=await fetch(api('/rest/getEndpoints.view')); let j=await r.json(); let eps=j['subsonic-response']?.endpoints||[]; let html=eps.map(e=>'<div class="bg-zinc-900 border border-zinc-800 rounded-xl p-3"><div class="flex justify-between"><div><div class="text-xs font-bold mono">'+e.path+' (Koyeb ENV)</div><div class="text-[10px] text-zinc-500">'+e.desc+' • '+e.method+'</div></div><button onclick="tryEp(\\''+e.path+'\\')" class="bg-white text-black px-3 py-1 rounded-full text-xs font-bold">Try</button></div></div>').join(''); document.getElementById('epList').innerHTML=html;}catch(e){document.getElementById('epList').textContent='Error:'+e}}
async function tryEp(path){let full=api(path); try{let r=await fetch(full);let t=await r.text();document.getElementById('epRes').textContent=full+' (Koyeb ENV)\\n\\n'+t.slice(0,5000);}catch(e){document.getElementById('epRes').textContent='Error:'+e}}
window.addEventListener('DOMContentLoaded',checkLogin);
</script>
</body></html>`

func main(){
	cfg=loadConfig(); db.load(); if len(db.Songs)==0&&cfg.TelegramFileID!=""{telegramDownloadDB(); db.load()}
	os.MkdirAll(filepath.Dir(cfg.DbPath),0755); initUsers()
	cookieContent:=getCookiesContent(); if cookieContent!=""{os.WriteFile("/tmp/cookies.txt",[]byte(cookieContent),0600)}
	mux:=http.NewServeMux()
	mux.HandleFunc("/rest/ping.view",handlePing); mux.HandleFunc("/rest/getLicense.view",handleLicense)
	mux.HandleFunc("/rest/getMusicFolders.view",handleMusicFolders); mux.HandleFunc("/rest/getUser.view",handleGetUser)
	mux.HandleFunc("/rest/getArtists.view",handleGetArtists); mux.HandleFunc("/rest/getArtist.view",handleGetArtist)
	mux.HandleFunc("/rest/getAlbum.view",handleGetAlbum); mux.HandleFunc("/rest/getSong.view",handleGetSong)
	mux.HandleFunc("/rest/search3.view",handleSearch3); mux.HandleFunc("/rest/getAlbumList2.view",handleAlbumList2)
	mux.HandleFunc("/rest/getRandomSongs.view",handleRandomSongs); mux.HandleFunc("/rest/stream.view",handleStream)
	mux.HandleFunc("/rest/download.view",handleDownload); mux.HandleFunc("/rest/getCoverArt.view",handleCoverArt)
	mux.HandleFunc("/rest/getPlaylists.view",handleGetPlaylists); mux.HandleFunc("/rest/getPlaylist.view",handleGetPlaylist)
	mux.HandleFunc("/rest/createPlaylist.view",handleCreatePlaylist); mux.HandleFunc("/rest/deletePlaylist.view",handleDeletePlaylist)
	mux.HandleFunc("/rest/updatePlaylist.view",handleUpdatePlaylist); mux.HandleFunc("/rest/importYoutubePlaylist.view",handleImportYT)
	mux.HandleFunc("/rest/createUser.view",handleCreateUser); mux.HandleFunc("/rest/getUsers.view",handleGetUsers)
	mux.HandleFunc("/rest/deleteUser.view",handleDeleteUser)
	mux.HandleFunc("/rest/star.view",handleStar); mux.HandleFunc("/rest/unstar.view",handleUnstar)
	mux.HandleFunc("/rest/getStarred.view",handleGetStarred)
	mux.HandleFunc("/rest/scrobble.view",handleScrobble); mux.HandleFunc("/rest/getScanStatus.view",handleScanStatus)
	mux.HandleFunc("/rest/getConnections.view",handleConnections)
	mux.HandleFunc("/rest/tgIndex.view",handleTgIndex)
	mux.HandleFunc("/rest/getEndpoints.view",handleEndpoints)
	mux.HandleFunc("/health",func(w http.ResponseWriter,r *http.Request){w.Header().Set("Access-Control-Allow-Origin","*"); cached:=0; db.RLock(); for _,s:=range db.Songs{if s.TgFileID!=""{cached++}}; db.RUnlock(); status:=checkConnections(); writeJSON(w,200,map[string]interface{}{"status":"ok","songs":len(db.Songs),"playlists":len(db.Playlists),"users":len(usersMap),"cached":cached,"connections":status, "env": map[string]interface{}{"singleSource": "Koyeb ENV only", "koyebUrl": r.Host, "hasYtKey": cfg.YtApiKey!="", "hasTgToken": cfg.TelegramToken!="", "hasCookies": getCookiesContent()!=""}})})
	mux.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){
		w.Header().Set("Content-Type","text/html")
		w.Header().Set("Access-Control-Allow-Origin","*")
		if r.URL.Path!="/"{http.NotFound(w,r); return}
		fmt.Fprint(w, adminHTML)
	})
	go func(){ticker:=time.NewTicker(5*time.Minute); for range ticker.C{db.save(); go telegramUploadDB()}}()
	port:=cfg.Port; if !strings.HasPrefix(port,":"){port=":"+port}; log.Printf("Starting PRO v4.1 KOYEB ENV SINGLE SOURCE on %s - UI uses Koyeb env only",port); log.Fatal(http.ListenAndServe(port,corsMiddleware(mux)))
}
