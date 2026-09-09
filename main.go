package main

import (
	"bytes"
	"crypto/md5"
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
	Port           string
	JioApiUrl      string
	YtApiKey       string
	YtCookies      string
	TelegramToken  string
	TelegramChatID string
	TelegramFileID string
	TelegramMusicChatID string // optional separate chat for music cache, defaults to same as DB chat
	SubUser        string
	SubPass        string
	SubUsersRaw    string
	DbPath         string
}
func env(k, d string) string { if v := os.Getenv(k); v != "" { return v }; return d }
func loadConfig() Config {
	c := Config{
		Port:           env("PORT", "8000"),
		JioApiUrl:      env("JIOSAAVN_API_URL", "https://jiosaavn-api-three-ashy.vercel.app"),
		YtApiKey:       os.Getenv("YT_API_KEY"),
		YtCookies:      os.Getenv("YT_COOKIES"),
		TelegramToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID: os.Getenv("TELEGRAM_CHAT_ID"),
		TelegramFileID: os.Getenv("TELEGRAM_DB_FILE_ID"),
		TelegramMusicChatID: env("TELEGRAM_MUSIC_CHAT_ID", ""),
		SubUser:        env("SUBSONIC_USER", "admin"),
		SubPass:        env("SUBSONIC_PASSWORD", "admin"),
		SubUsersRaw:    os.Getenv("SUBSONIC_USERS"),
		DbPath:         env("DB_PATH", "db.json"),
	}
	c.JioApiUrl = strings.TrimSuffix(c.JioApiUrl, "/")
	if c.TelegramMusicChatID == "" { c.TelegramMusicChatID = c.TelegramChatID }
	return c
}
var cfg Config
var usersMap = map[string]string{}
var muUsers sync.RWMutex
func initUsers() {
	muUsers.Lock()
	defer muUsers.Unlock()
	usersMap = map[string]string{}
	usersMap[cfg.SubUser] = cfg.SubPass
	if cfg.SubUsersRaw != "" {
		for _, p := range strings.Split(cfg.SubUsersRaw, ",") {
			p = strings.TrimSpace(p)
			if p == "" { continue }
			kv := strings.SplitN(p, ":", 2)
			if len(kv)==2 { usersMap[strings.TrimSpace(kv[0])]=strings.TrimSpace(kv[1]) }
		}
	}
	if db.Users != nil {
		for u,pw := range db.Users { if _,ok:=usersMap[u]; !ok { usersMap[u]=pw } }
	}
	log.Printf("Users loaded %d", len(usersMap))
}

type Song struct {
	ID string `json:"id"`; Title string `json:"title"`; Artist string `json:"artist"`; ArtistID string `json:"artistId"`; Album string `json:"album"`; AlbumID string `json:"albumId"`; Duration int `json:"duration"`; CoverArt string `json:"coverArt"`; Year int `json:"year"`; Genre string `json:"genre"`; Source string `json:"source"`; SourceID string `json:"sourceId"`; StreamURL string `json:"streamUrl,omitempty"`; ThumbURL string `json:"thumbUrl"`; PlayCount int `json:"playCount"`; TgFileID string `json:"tgFileId,omitempty"`; TgFileUniqueID string `json:"tgFileUniqueId,omitempty"`; CachedAt string `json:"cachedAt,omitempty"`
}
type Artist struct{ ID string `json:"id"`; Name string `json:"name"` }
type Album struct{ ID string `json:"id"`; Name string `json:"name"`; Artist string `json:"artist"`; ArtistID string `json:"artistId"`; CoverArt string `json:"coverArt"`; SongIDs []string `json:"songIds"`; ThumbURL string `json:"thumbUrl"`; Year int `json:"year"` }
type Playlist struct{ ID string `json:"id"`; Name string `json:"name"`; Owner string `json:"owner"`; Comment string `json:"comment"`; SongIDs []string `json:"songIds"`; Public bool `json:"public"`; Created string `json:"created"`; Source string `json:"source"`; SourceURL string `json:"sourceUrl"` }
type DB struct {
	sync.RWMutex
	Songs map[string]*Song `json:"songs"`; Artists map[string]*Artist `json:"artists"`; Albums map[string]*Album `json:"albums"`; Playlists map[string]*Playlist `json:"playlists"`; Users map[string]string `json:"users"`; Starred map[string]map[string]bool `json:"starred"`
}
var db = &DB{Songs: make(map[string]*Song), Artists: make(map[string]*Artist), Albums: make(map[string]*Album), Playlists: make(map[string]*Playlist), Users: make(map[string]string), Starred: make(map[string]map[string]bool)}
var streamCache sync.Map
type cachedURL struct{ URL string; Expiry time.Time }
var ytSem = make(chan struct{}, 2)
var tgCacheInProgress sync.Map // songID -> bool to avoid duplicate uploads
var httpClient = &http.Client{Timeout: 15 * time.Second}

func (d *DB) save() error {
	d.RLock(); defer d.RUnlock()
	b,_:=json.MarshalIndent(d,"","  ")
	tmp:=cfg.DbPath+".tmp"
	os.WriteFile(tmp,b,0644)
	return os.Rename(tmp,cfg.DbPath)
}
func (d *DB) load() error {
	if _,err:=os.Stat(cfg.DbPath); err!=nil { return err }
	b,_:=os.ReadFile(cfg.DbPath)
	var tmp DB
	json.Unmarshal(b,&tmp)
	d.Lock()
	if tmp.Songs!=nil { d.Songs=tmp.Songs }
	if tmp.Artists!=nil { d.Artists=tmp.Artists }
	if tmp.Albums!=nil { d.Albums=tmp.Albums }
	if tmp.Playlists!=nil { d.Playlists=tmp.Playlists }
	if tmp.Users!=nil { d.Users=tmp.Users }
	if tmp.Starred!=nil { d.Starred=tmp.Starred }
	if d.Starred==nil { d.Starred=make(map[string]map[string]bool) }
	d.Unlock()
	return nil
}

// Telegram DB backup
func telegramUploadDB(){
	if cfg.TelegramToken==""||cfg.TelegramChatID=="" { return }
	if _,err:=os.Stat(cfg.DbPath); err!=nil { return }
	f,_:=os.Open(cfg.DbPath)
	defer f.Close()
	body:=&bytes.Buffer{}
	w:=multipart.NewWriter(body)
	w.WriteField("chat_id",cfg.TelegramChatID)
	w.WriteField("caption",fmt.Sprintf("backup %s songs:%d pls:%d users:%d cached:%d", time.Now().Format(time.RFC3339), len(db.Songs), len(db.Playlists), len(db.Users), countCached()))
	part,_:=w.CreateFormFile("document","db.json")
	io.Copy(part,f)
	w.Close()
	url:=fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument",cfg.TelegramToken)
	req,_:=http.NewRequest("POST",url,body)
	req.Header.Set("Content-Type",w.FormDataContentType())
	client:=&http.Client{Timeout:30*time.Second}
	resp,_:=client.Do(req)
	if resp!=nil { defer resp.Body.Close(); b,_:=io.ReadAll(resp.Body); if len(b)>400 { b=b[:400] }; log.Println("tg db upload",string(b)) }
}
func countCached()int{ c:=0; db.RLock(); for _,s:=range db.Songs{ if s.TgFileID!=""{ c++ } }; db.RUnlock(); return c }
func telegramDownloadDB() error {
	if cfg.TelegramToken==""||cfg.TelegramFileID=="" { return fmt.Errorf("no file_id") }
	u:=fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s",cfg.TelegramToken,cfg.TelegramFileID)
	resp,err:=httpClient.Get(u)
	if err!=nil { return err }
	defer resp.Body.Close()
	var r struct{ Ok bool; Result struct{ FilePath string `json:"file_path"` } `json:"result"` }
	json.NewDecoder(resp.Body).Decode(&r)
	if r.Result.FilePath=="" { return fmt.Errorf("empty path") }
	down:=fmt.Sprintf("https://api.telegram.org/file/bot%s/%s",cfg.TelegramToken,r.Result.FilePath)
	resp2,_:=httpClient.Get(down)
	defer resp2.Body.Close()
	b,_:=io.ReadAll(resp2.Body)
	return os.WriteFile(cfg.DbPath,b,0644)
}

// Telegram music cache functions
func telegramGetFileUrl(fileID string)(string,error){
	if cfg.TelegramToken=="" { return "",fmt.Errorf("no token") }
	u:=fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s",cfg.TelegramToken,fileID)
	resp,err:=httpClient.Get(u)
	if err!=nil { return "",err }
	defer resp.Body.Close()
	var r struct{ Ok bool; Result struct{ FilePath string `json:"file_path"` } `json:"result"` }
	if err:=json.NewDecoder(resp.Body).Decode(&r); err!=nil { return "",err }
	if r.Result.FilePath=="" { return "",fmt.Errorf("empty file_path") }
	return fmt.Sprintf("https://api.telegram.org/file/bot%s/%s",cfg.TelegramToken,r.Result.FilePath),nil
}
func telegramUploadAudioFile(filePathOrUrl string, song *Song)(string,string,error){
	// filePathOrUrl can be http url (yt direct) or local path
	// we will download to memory if url, max 50MB
	if cfg.TelegramToken==""||cfg.TelegramMusicChatID=="" { return "","",fmt.Errorf("tg not configured") }
	var reader io.Reader
	var filename string
	filename = fmt.Sprintf("%s - %s.m4a", song.Artist, song.Title)
	filename = strings.ReplaceAll(filename, "/", "_")
	filename = strings.ReplaceAll(filename, "\"", "")

	var data []byte
	if strings.HasPrefix(filePathOrUrl,"http"){
		resp,err:=httpClient.Get(filePathOrUrl)
		if err!=nil { return "","",err }
		defer resp.Body.Close()
		// limit 48MB
		limited:=io.LimitReader(resp.Body, 48*1024*1024)
		data, _ = io.ReadAll(limited)
		reader = bytes.NewReader(data)
	} else {
		f,err:=os.Open(filePathOrUrl)
		if err!=nil { return "","",err }
		defer f.Close()
		data,_=io.ReadAll(io.LimitReader(f, 48*1024*1024))
		reader = bytes.NewReader(data)
	}

	body:=&bytes.Buffer{}
	w:=multipart.NewWriter(body)
	w.WriteField("chat_id",cfg.TelegramMusicChatID)
	w.WriteField("caption",fmt.Sprintf("🎵 %s - %s | %s | %s", song.Artist, song.Title, song.ID, time.Now().Format("2006-01-02")))
	// Telegram prefers audio or document - use audio for better player
	part,_:=w.CreateFormFile("audio", filename)
	io.Copy(part, reader)
	w.Close()

	apiUrl:=fmt.Sprintf("https://api.telegram.org/bot%s/sendAudio",cfg.TelegramToken)
	req,_:=http.NewRequest("POST",apiUrl,body)
	req.Header.Set("Content-Type",w.FormDataContentType())
	client:=&http.Client{Timeout: 60*time.Second}
	resp,err:=client.Do(req)
	if err!=nil { return "","",err }
	defer resp.Body.Close()
	b,_:=io.ReadAll(resp.Body)
	// parse response
	var res struct{
		Ok bool `json:"ok"`
		Result struct{
			Audio struct{ FileID string `json:"file_id"`; FileUniqueID string `json:"file_unique_id"` } `json:"audio"`
			Document struct{ FileID string `json:"file_id"`; FileUniqueID string `json:"file_unique_id"` } `json:"document"`
		} `json:"result"`
		Description string `json:"description"`
	}
	json.Unmarshal(b,&res)
	if !res.Ok{
		// try as document if audio fails (sometimes m4a not recognized)
		// retry as document
		body2:=&bytes.Buffer{}
		w2:=multipart.NewWriter(body2)
		w2.WriteField("chat_id",cfg.TelegramMusicChatID)
		w2.WriteField("caption",fmt.Sprintf("🎵 %s - %s | %s", song.Artist, song.Title, song.ID))
		part2,_:=w2.CreateFormFile("document", filename)
		part2.Write(data)
		w2.Close()
		apiUrl2:=fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument",cfg.TelegramToken)
		req2,_:=http.NewRequest("POST",apiUrl2,body2)
		req2.Header.Set("Content-Type",w2.FormDataContentType())
		resp2,err2:=client.Do(req2)
		if err2!=nil { return "","",fmt.Errorf("audio fail: %s doc err: %v", res.Description, err2) }
		defer resp2.Body.Close()
		b2,_:=io.ReadAll(resp2.Body)
		var res2 struct{ Ok bool; Result struct{ Document struct{ FileID string `json:"file_id"`; FileUniqueID string `json:"file_unique_id"` } `json:"document"` } `json:"result"`; Description string `json:"description"` }
		json.Unmarshal(b2,&res2)
		if !res2.Ok{ return "","",fmt.Errorf("tg upload failed audio:%s doc:%s", res.Description, res2.Description) }
		return res2.Result.Document.FileID, res2.Result.Document.FileUniqueID, nil
	}
	fid:=res.Result.Audio.FileID
	if fid==""{ fid=res.Result.Document.FileID }
	fuid:=res.Result.Audio.FileUniqueID
	if fuid==""{ fuid=res.Result.Document.FileUniqueID }
	if fid==""{ return "","",fmt.Errorf("no file_id in response %s", string(b)) }
	return fid,fuid,nil
}
func cacheSongToTelegram(s *Song, streamUrl string){
	if s.TgFileID!="" { return }
	if _,inProg:=tgCacheInProgress.Load(s.ID); inProg { return }
	tgCacheInProgress.Store(s.ID,true)
	defer tgCacheInProgress.Delete(s.ID)

	log.Printf("Caching to TG: %s - %s (%s)", s.Artist, s.Title, s.ID)
	fid,fuid,err:=telegramUploadAudioFile(streamUrl, s)
	if err!=nil{
		log.Printf("TG cache failed for %s: %v", s.ID, err)
		return
	}
	db.Lock()
	if song,ok:=db.Songs[s.ID]; ok{
		song.TgFileID=fid
		song.TgFileUniqueID=fuid
		song.CachedAt=time.Now().Format(time.RFC3339)
	}
	db.Unlock()
	db.save()
	go telegramUploadDB()
	log.Printf("Cached %s to TG file_id %s", s.ID, fid[:20])
}

func checkAuth(r *http.Request)(bool,string){
	q:=r.URL.Query()
	u:=q.Get("u"); if u=="" { u=q.Get("username") }
	p:=q.Get("p"); t:=q.Get("t"); s:=q.Get("s")
	if u==""{
		if strings.Contains(r.URL.Path,"ping") { return true,cfg.SubUser }
		muUsers.RLock()
		if len(usersMap)==1 { for user:=range usersMap { muUsers.RUnlock(); return true,user } }
		muUsers.RUnlock()
		return false,""
	}
	muUsers.RLock(); exp,ok:=usersMap[u]; muUsers.RUnlock()
	if !ok { return false,"" }
	if t!=""&&s!=""{
		h:=md5.New(); h.Write([]byte(exp+s))
		if fmt.Sprintf("%x",h.Sum(nil))==t { return true,u }
		return false,""
	}
	if p!=""{
		if strings.HasPrefix(p,"enc:"){ if d,err:=hex.DecodeString(p[4:]); err==nil { p=string(d) } }
		if p==exp { return true,u }
	}
	return false,""
}
func writeJSON(w http.ResponseWriter,s int,p interface{}){ w.Header().Set("Content-Type","application/json"); w.WriteHeader(s); json.NewEncoder(w).Encode(p) }
func subOK(d map[string]interface{})map[string]interface{}{
	b:=map[string]interface{}{"status":"ok","version":"1.16.1","type":"go-jio-yt-tgcache","serverVersion":"3.0-tg-cache","openSubsonic":true}
	for k,v:=range d{ b[k]=v }
	return map[string]interface{}{"subsonic-response":b}
}
func subFail(m string,c int)map[string]interface{}{
	return map[string]interface{}{"subsonic-response":map[string]interface{}{"status":"failed","version":"1.16.1","error":map[string]interface{}{"code":c,"message":m}}}
}
func respond(w http.ResponseWriter,r *http.Request,d map[string]interface{}){ writeJSON(w,200,subOK(d)) }

func slugArtist(n string)string{ return "ar_"+url.PathEscape(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(n)," ","_"))) }
func slugAlbum(n,a string)string{ return "al_"+url.PathEscape(strings.ToLower(strings.ReplaceAll(a+"_"+n," ","_"))) }
func ensureArtist(n string)*Artist{
	if n==""{ n="Unknown Artist" }
	id:=slugArtist(n)
	db.RLock(); if a,ok:=db.Artists[id]; ok{ db.RUnlock(); return a }; db.RUnlock()
	a:=&Artist{ID:id,Name:n}
	db.Lock(); db.Artists[id]=a; db.Unlock()
	return a
}
func ensureAlbum(name,artist,artistId,thumb string)*Album{
	if name==""{ name="Unknown Album" }
	id:=slugAlbum(name,artist)
	db.RLock(); if al,ok:=db.Albums[id]; ok{ db.RUnlock(); return al }; db.RUnlock()
	al:=&Album{ID:id,Name:name,Artist:artist,ArtistID:artistId,CoverArt:id,ThumbURL:thumb,SongIDs:[]string{}}
	db.Lock(); db.Albums[id]=al; db.Unlock()
	return al
}
func contains(arr []string,s string)bool{ for _,v:=range arr{ if v==s{ return true } }; return false }

func parseJioImage(img interface{})string{
	switch v:=img.(type){
	case string: return v
	case []interface{}:
		if len(v)>0{ if m,ok:=v[len(v)-1].(map[string]interface{}); ok{ if u,ok:=m["url"].(string); ok{ return u } } }
	}
	return ""
}
func parseJioDownloadUrl(d interface{})string{
	if s,ok:=d.(string); ok{ return s }
	if arr,ok:=d.([]interface{}); ok{
		var best string
		for _,it:=range arr{
			if m,ok:=it.(map[string]interface{}); ok{
				link,_:=m["link"].(string)
				if link==""{ link,_=m["url"].(string) }
				if q,_:=m["quality"].(string); strings.Contains(q,"320"){ return link }
				best=link
			}
		}
		return best
	}
	return ""
}
func searchJio(query string)([]*Song,error){
	if query==""{ return nil,nil }
	ep:=fmt.Sprintf("%s/search?query=%s",cfg.JioApiUrl,url.QueryEscape(query))
	resp,err:=httpClient.Get(ep)
	if err!=nil{ return nil,err }
	defer resp.Body.Close()
	body,_:=io.ReadAll(resp.Body)
	var g map[string]interface{}
	json.Unmarshal(body,&g)
	var raw []interface{}
	if data,ok:=g["data"].(map[string]interface{}); ok{
		if res,ok:=data["results"].([]interface{}); ok{ raw=res }
	}
	var songs []*Song
	for _,rs:=range raw{
		m,_:=rs.(map[string]interface{})
		title,_:=m["name"].(string)
		if title==""{ continue }
		artist,_:=m["primaryArtists"].(string)
		if artist==""{ artist="Various" }
		id,_:=m["id"].(string)
		thumb:=parseJioImage(m["image"])
		artistObj:=ensureArtist(artist)
		albumObj:=ensureAlbum(title+" Single",artist,artistObj.ID,thumb)
		s:=&Song{ID:"jio_"+id,Title:title,Artist:artist,ArtistID:artistObj.ID,Album:albumObj.Name,AlbumID:albumObj.ID,CoverArt:albumObj.ID,Source:"jio",SourceID:id,ThumbURL:thumb}
		if dl,ok:=m["downloadUrl"]; ok{ s.StreamURL=parseJioDownloadUrl(dl) }
		songs=append(songs,s)
		db.Lock()
		db.Songs[s.ID]=s
		if !contains(albumObj.SongIDs,s.ID){ albumObj.SongIDs=append(albumObj.SongIDs,s.ID) }
		db.Unlock()
	}
	if len(songs)>0{ go func(){ db.save(); go telegramUploadDB() }() }
	return songs,nil
}
func getJioStream(jioId string)(string,error){
	if v,ok:=streamCache.Load("jio_"+jioId); ok{
		c:=v.(cachedURL)
		if time.Now().Before(c.Expiry){ return c.URL,nil }
	}
	ep:=fmt.Sprintf("%s/songs?id=%s",cfg.JioApiUrl,jioId)
	resp,_:=httpClient.Get(ep)
	if resp!=nil{
		defer resp.Body.Close()
		body,_:=io.ReadAll(resp.Body)
		var g map[string]interface{}
		json.Unmarshal(body,&g)
		if data,ok:=g["data"].([]interface{}); ok{
			for _,it:=range data{
				if m,ok:=it.(map[string]interface{}); ok{
					if dl,ok:=m["downloadUrl"]; ok{
						u:=parseJioDownloadUrl(dl)
						if u!=""{
							streamCache.Store("jio_"+jioId,cachedURL{URL:u,Expiry:time.Now().Add(time.Hour)})
							return u,nil
						}
					}
				}
			}
		}
	}
	return "",fmt.Errorf("jio not found")
}
type YTSearch struct{
	Items []struct{
		ID struct{ VideoID string `json:"videoId"` } `json:"id"`
		Snippet struct{
			Title string `json:"title"`; ChannelTitle string `json:"channelTitle"`
			Thumbnails map[string]struct{ URL string `json:"url"` } `json:"thumbnails"`
		} `json:"snippet"`
	} `json:"items"`
}
func searchYoutube(q string)([]*Song,error){
	if cfg.YtApiKey==""{ return nil,fmt.Errorf("no key") }
	u:=fmt.Sprintf("https://www.googleapis.com/youtube/v3/search?part=snippet&type=video&videoCategoryId=10&maxResults=15&q=%s&key=%s",url.QueryEscape(q),cfg.YtApiKey)
	resp,_:=httpClient.Get(u)
	if resp==nil{ return nil,fmt.Errorf("yt fail") }
	defer resp.Body.Close()
	var yt YTSearch
	json.NewDecoder(resp.Body).Decode(&yt)
	var songs []*Song
	for _,it:=range yt.Items{
		if it.ID.VideoID==""{ continue }
		thumb:=""
		if t,ok:=it.Snippet.Thumbnails["high"]; ok{ thumb=t.URL }
		art:=ensureArtist(it.Snippet.ChannelTitle)
		al:=ensureAlbum(it.Snippet.ChannelTitle+" - YouTube",it.Snippet.ChannelTitle,art.ID,thumb)
		s:=&Song{ID:"yt_"+it.ID.VideoID,Title:it.Snippet.Title,Artist:it.Snippet.ChannelTitle,ArtistID:art.ID,Album:al.Name,AlbumID:al.ID,CoverArt:al.ID,Source:"yt",SourceID:it.ID.VideoID,ThumbURL:thumb}
		songs=append(songs,s)
		db.Lock()
		if existing,ok:=db.Songs[s.ID]; ok{
			// keep TgFileID if already cached
			if existing.TgFileID!=""{
				s.TgFileID=existing.TgFileID
				s.TgFileUniqueID=existing.TgFileUniqueID
				s.CachedAt=existing.CachedAt
			}
		}
		db.Songs[s.ID]=s
		if !contains(al.SongIDs,s.ID){ al.SongIDs=append(al.SongIDs,s.ID) }
		db.Unlock()
	}
	go func(){ db.save(); go telegramUploadDB() }()
	return songs,nil
}
func getYoutubeStream(vid string)(string,error){
	if v,ok:=streamCache.Load("yt_"+vid); ok{
		c:=v.(cachedURL)
		if time.Now().Before(c.Expiry){ return c.URL,nil }
	}
	ytSem<-struct{}{}
	defer func(){ <-ytSem }()
	cookie:=""
	if cfg.YtCookies!=""{
		cookie="/tmp/cookies.txt"
		os.WriteFile(cookie,[]byte(cfg.YtCookies),0600)
	}
	args:=[]string{"--no-playlist","--get-url","-f","bestaudio[ext=m4a]/bestaudio","https://www.youtube.com/watch?v="+vid}
	if cookie!=""{ args=append([]string{"--cookies",cookie},args...) }
	cmd:=exec.Command("yt-dlp",args...)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout=&out
	cmd.Stderr=&errBuf
	done:=make(chan error,1)
	go func(){ done<-cmd.Run() }()
	select{
	case err:=<-done:
		if err!=nil{ return "",fmt.Errorf("%v %s",err,errBuf.String()) }
	case <-time.After(20*time.Second):
		cmd.Process.Kill()
		return "",fmt.Errorf("timeout")
	}
	for _,line:=range strings.Split(strings.TrimSpace(out.String()),"\n"){
		if strings.HasPrefix(strings.TrimSpace(line),"http"){
			streamCache.Store("yt_"+vid,cachedURL{URL:strings.TrimSpace(line),Expiry:time.Now().Add(30*time.Minute)})
			return strings.TrimSpace(line),nil
		}
	}
	return "",fmt.Errorf("no url")
}
func extractPlaylistID(input string)string{
	if strings.HasPrefix(input,"PL")&&!strings.Contains(input,"http"){ return input }
	u,err:=url.Parse(input)
	if err==nil{
		if list:=u.Query().Get("list"); list!=""{ return list }
	}
	re:=regexp.MustCompile(`[?&]list=([a-zA-Z0-9_-]+)`)
	m:=re.FindStringSubmatch(input)
	if len(m)>1{ return m[1] }
	return input
}
func importYoutubePlaylist(pid string,owner string)(*Playlist,error){
	if cfg.YtApiKey==""{ return nil,fmt.Errorf("YT_API_KEY missing") }
	pid=extractPlaylistID(pid)
	title:=pid
	infoURL:=fmt.Sprintf("https://www.googleapis.com/youtube/v3/playlists?part=snippet&id=%s&key=%s",pid,cfg.YtApiKey)
	if resp,err:=httpClient.Get(infoURL); err==nil{
		defer resp.Body.Close()
		var pr struct{ Items []struct{ Snippet struct{ Title string `json:"title"` } `json:"snippet"` } `json:"items"` }
		json.NewDecoder(resp.Body).Decode(&pr)
		if len(pr.Items)>0{ title=pr.Items[0].Snippet.Title }
	}
	var allIDs []string
	pageToken:=""
	for{
		apiUrl:=fmt.Sprintf("https://www.googleapis.com/youtube/v3/playlistItems?part=snippet&maxResults=50&playlistId=%s&key=%s",pid,cfg.YtApiKey)
		if pageToken!=""{ apiUrl+="&pageToken="+pageToken }
		resp,err:=httpClient.Get(apiUrl)
		if err!=nil{ break }
		var pr struct{
			NextPageToken string `json:"nextPageToken"`
			Items []struct{ Snippet struct{
				ResourceID struct{ VideoID string `json:"videoId"` } `json:"resourceId"`
				Title string `json:"title"`; ChannelTitle string `json:"channelTitle"`
				Thumbnails map[string]struct{ URL string `json:"url"` } `json:"thumbnails"`
			} `json:"snippet"` } `json:"items"`
		}
		json.NewDecoder(resp.Body).Decode(&pr)
		resp.Body.Close()
		for _,it:=range pr.Items{
			if it.Snippet.ResourceID.VideoID==""{ continue }
			vid:=it.Snippet.ResourceID.VideoID
			allIDs=append(allIDs,vid)
			thumb:=""
			if t,ok:=it.Snippet.Thumbnails["high"]; ok{ thumb=t.URL }
			art:=ensureArtist(it.Snippet.ChannelTitle)
			al:=ensureAlbum(it.Snippet.ChannelTitle+" - YouTube",it.Snippet.ChannelTitle,art.ID,thumb)
			sID:="yt_"+vid
			s:=&Song{ID:sID,Title:it.Snippet.Title,Artist:it.Snippet.ChannelTitle,ArtistID:art.ID,Album:al.Name,AlbumID:al.ID,CoverArt:al.ID,Source:"yt",SourceID:vid,ThumbURL:thumb}
			db.Lock()
			if existing,ok:=db.Songs[s.ID]; ok && existing.TgFileID!=""{
				s.TgFileID=existing.TgFileID
				s.TgFileUniqueID=existing.TgFileUniqueID
				s.CachedAt=existing.CachedAt
			}
			db.Songs[s.ID]=s
			if !contains(al.SongIDs,s.ID){ al.SongIDs=append(al.SongIDs,s.ID) }
			db.Unlock()
		}
		if pr.NextPageToken==""{ break }
		pageToken=pr.NextPageToken
		if len(allIDs)>500{ break }
	}
	if len(allIDs)==0{ return nil,fmt.Errorf("no videos") }
	plID:="pl_"+pid
	pl:=&Playlist{ID:plID,Name:title,Owner:owner,Comment:fmt.Sprintf("YT Import %d songs - %s - TG Cache enabled",len(allIDs),owner),SongIDs:[]string{},Created:time.Now().Format(time.RFC3339),Source:"yt",SourceURL:"https://www.youtube.com/playlist?list="+pid}
	for _,vid:=range allIDs{ pl.SongIDs=append(pl.SongIDs,"yt_"+vid) }
	db.Lock(); db.Playlists[pl.ID]=pl; db.Unlock()
	db.save(); go telegramUploadDB()
	return pl,nil
}

// handlers
func handlePing(w http.ResponseWriter,r *http.Request){ respond(w,r,map[string]interface{}{}) }
func handleLicense(w http.ResponseWriter,r *http.Request){ respond(w,r,map[string]interface{}{"license":map[string]interface{}{"valid":true}}) }
func handleMusicFolders(w http.ResponseWriter,r *http.Request){ respond(w,r,map[string]interface{}{"musicFolders":map[string]interface{}{"musicFolder":[]map[string]interface{}{{"id":0,"name":"Music"},{"id":1,"name":"TG Cached"}}}}) }
func handleGetUser(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
	respond(w,r,map[string]interface{}{"user":map[string]interface{}{"username":user,"adminRole":user==cfg.SubUser,"playlistRole":true,"jukeboxRole":false,"streamRole":true,"downloadRole":true}})
}
func handleGetArtists(w http.ResponseWriter,r *http.Request){
	db.RLock(); defer db.RUnlock()
	idx:=map[string][]map[string]interface{}{}
	for _,a:=range db.Artists{
		letter:="#"; if len(a.Name)>0{ letter=strings.ToUpper(string(a.Name[0])) }
		idx[letter]=append(idx[letter],map[string]interface{}{"id":a.ID,"name":a.Name})
	}
	var indexes []map[string]interface{}
	for k,v:=range idx{ indexes=append(indexes,map[string]interface{}{"name":k,"artist":v}) }
	respond(w,r,map[string]interface{}{"artists":map[string]interface{}{"index":indexes}})
}
func handleGetArtist(w http.ResponseWriter,r *http.Request){
	id:=r.URL.Query().Get("id")
	db.RLock(); a,ok:=db.Artists[id]; if !ok{ db.RUnlock(); writeJSON(w,200,subFail("not found",70)); return }
	var albums []map[string]interface{}
	for _,al:=range db.Albums{ if al.ArtistID==id{ albums=append(albums,map[string]interface{}{"id":al.ID,"name":al.Name,"songCount":len(al.SongIDs),"coverArt":al.ID}) } }
	db.RUnlock()
	respond(w,r,map[string]interface{}{"artist":map[string]interface{}{"id":a.ID,"name":a.Name,"album":albums}})
}
func handleGetAlbum(w http.ResponseWriter,r *http.Request){
	id:=r.URL.Query().Get("id")
	db.RLock()
	al,ok:=db.Albums[id]
	if !ok{ if s,ok2:=db.Songs[id]; ok2{ al,ok=db.Albums[s.AlbumID] } }
	if !ok{ db.RUnlock(); writeJSON(w,200,subFail("not found",70)); return }
	var songs []map[string]interface{}
	for _,sid:=range al.SongIDs{ if s,ok:=db.Songs[sid]; ok{ songs=append(songs,songToSubsonic(s,r)) } }
	db.RUnlock()
	respond(w,r,map[string]interface{}{"album":map[string]interface{}{"id":al.ID,"name":al.Name,"artist":al.Artist,"coverArt":al.ID,"song":songs,"songCount":len(songs)}})
}
func handleGetSong(w http.ResponseWriter,r *http.Request){
	id:=r.URL.Query().Get("id")
	db.RLock(); s,ok:=db.Songs[id]; db.RUnlock()
	if !ok{ writeJSON(w,200,subFail("not found",70)); return }
	respond(w,r,map[string]interface{}{"song":songToSubsonic(s,r)})
}
func songToSubsonic(s *Song,r *http.Request)map[string]interface{}{
	starred:=false
	if r!=nil{
		if ok,user:=checkAuth(r); ok{
			db.RLock()
			if m,ok:=db.Starred[user]; ok{
				if m[s.ID]{ starred=true }
			}
			db.RUnlock()
		}
	}
	m:=map[string]interface{}{"id":s.ID,"title":s.Title,"artist":s.Artist,"album":s.Album,"albumId":s.AlbumID,"artistId":s.ArtistID,"coverArt":s.CoverArt,"duration":s.Duration,"contentType":"audio/mp4","suffix":"m4a","year":s.Year,"genre":s.Genre,"playCount":s.PlayCount}
	if starred{ m["starred"]=time.Now().Format(time.RFC3339) }
	if s.TgFileID!=""{ m["cached"]="tg" }
	return m
}
func handleSearch3(w http.ResponseWriter,r *http.Request){
	if ok,_:=checkAuth(r); !ok{ writeJSON(w,200,subFail("auth",40)); return }
	q:=r.URL.Query().Get("query")
	sCount,_:=strconv.Atoi(r.URL.Query().Get("songCount")); if sCount==0{ sCount=50 }
	var jio,yt []*Song
	var wg sync.WaitGroup
	wg.Add(2)
	go func(){ defer wg.Done(); jio,_=searchJio(q) }()
	go func(){ defer wg.Done(); yt,_=searchYoutube(q) }()
	wg.Wait()
	all:=append(jio,yt...)
	if len(all)>sCount{ all=all[:sCount] }
	var res []map[string]interface{}
	for _,s:=range all{ res=append(res,songToSubsonic(s,r)) }
	respond(w,r,map[string]interface{}{"searchResult3":map[string]interface{}{"song":res}})
}
func handleAlbumList2(w http.ResponseWriter,r *http.Request){
	db.RLock(); var albums []*Album; for _,al:=range db.Albums{ albums=append(albums,al) }; db.RUnlock()
	rand.Shuffle(len(albums),func(i,j int){ albums[i],albums[j]=albums[j],albums[i] })
	if len(albums)>20{ albums=albums[:20] }
	var res []map[string]interface{}
	for _,al:=range albums{ res=append(res,map[string]interface{}{"id":al.ID,"name":al.Name,"artist":al.Artist,"coverArt":al.ID,"songCount":len(al.SongIDs)}) }
	respond(w,r,map[string]interface{}{"albumList2":map[string]interface{}{"album":res}})
}
func handleRandomSongs(w http.ResponseWriter,r *http.Request){
	db.RLock(); var all []*Song; for _,s:=range db.Songs{ all=append(all,s) }; db.RUnlock()
	rand.Shuffle(len(all),func(i,j int){ all[i],all[j]=all[j],all[i] })
	if len(all)>20{ all=all[:20] }
	var res []map[string]interface{}
	for _,s:=range all{ res=append(res,songToSubsonic(s,r)) }
	respond(w,r,map[string]interface{}{"randomSongs":map[string]interface{}{"song":res}})
}
func handleStream(w http.ResponseWriter,r *http.Request){
	if ok,_:=checkAuth(r); !ok{ writeJSON(w,200,subFail("auth",40)); return }
	id:=r.URL.Query().Get("id")
	db.RLock(); s,ok:=db.Songs[id]; db.RUnlock()
	if !ok{ http.Error(w,"not found",404); return }
	db.Lock(); if _,ok:=db.Songs[id]; ok{ db.Songs[id].PlayCount++ }; db.Unlock()

	// 1st priority: TG cached file
	if s.TgFileID!=""{
		if tgUrl,err:=telegramGetFileUrl(s.TgFileID); err==nil{
			log.Printf("Serving TG cached: %s", s.ID)
			req,_:=http.NewRequest("GET",tgUrl,nil)
			resp,err:=httpClient.Do(req)
			if err==nil{
				defer resp.Body.Close()
				w.Header().Set("Content-Type","audio/mp4")
				w.Header().Set("X-Cache","TG")
				io.Copy(w,resp.Body)
				return
			}
		}
	}

	// 2nd: YT/Jio direct
	var urlStr string; var err error
	if s.Source=="jio"{ urlStr,_=getJioStream(s.SourceID); if urlStr==""{ urlStr=s.StreamURL } }else{ urlStr,err=getYoutubeStream(s.SourceID) }
	if err!=nil||urlStr==""{ http.Error(w,"stream fail",502); return }

	// Trigger TG cache in background on first play if not cached
	if s.TgFileID=="" && s.Source=="yt"{
		go cacheSongToTelegram(s, urlStr)
	}

	req,_:=http.NewRequest("GET",urlStr,nil); req.Header.Set("User-Agent","Mozilla/5.0")
	resp,err:=httpClient.Do(req)
	if err!=nil{ http.Error(w,"upstream",502); return }
	defer resp.Body.Close()
	w.Header().Set("Content-Type","audio/mp4")
	w.Header().Set("X-Cache","MISS")
	io.Copy(w,resp.Body)
}
func handleDownload(w http.ResponseWriter,r *http.Request){
	if ok,_:=checkAuth(r); !ok{ writeJSON(w,200,subFail("auth",40)); return }
	id:=r.URL.Query().Get("id")
	db.RLock(); s,ok:=db.Songs[id]; db.RUnlock()
	if !ok{ http.Error(w,"not found",404); return }

	var urlStr string
	if s.TgFileID!=""{
		if tgUrl,err:=telegramGetFileUrl(s.TgFileID); err==nil{
			urlStr=tgUrl
		}
	}
	if urlStr==""{
		if s.Source=="jio"{ urlStr,_=getJioStream(s.SourceID); if urlStr==""{ urlStr=s.StreamURL } }else{ var err error; urlStr,err=getYoutubeStream(s.SourceID); if err!=nil||urlStr==""{ http.Error(w,"stream fail",502); return } }
	}
	req,_:=http.NewRequest("GET",urlStr,nil); req.Header.Set("User-Agent","Mozilla/5.0")
	resp,err:=httpClient.Do(req)
	if err!=nil{ http.Error(w,"upstream",502); return }
	defer resp.Body.Close()
	w.Header().Set("Content-Type","audio/mp4")
	w.Header().Set("Content-Disposition",fmt.Sprintf("attachment; filename=\"%s - %s.m4a\"", s.Artist, s.Title))
	io.Copy(w,resp.Body)
}
func handleCoverArt(w http.ResponseWriter,r *http.Request){
	id:=r.URL.Query().Get("id")
	var thumb string
	db.RLock()
	if al,ok:=db.Albums[id]; ok{ thumb=al.ThumbURL }else if s,ok:=db.Songs[id]; ok{ thumb=s.ThumbURL }else{
		for _,al:=range db.Albums{ if al.ID==id{ thumb=al.ThumbURL; break } }
		for _,s:=range db.Songs{ if s.ID==id{ thumb=s.ThumbURL; break } }
	}
	db.RUnlock()
	if thumb==""{ http.Redirect(w,r,"https://via.placeholder.com/500x500.png?text=No+Art",302); return }
	resp,_:=httpClient.Get(thumb)
	if resp==nil{ http.Redirect(w,r,thumb,302); return }
	defer resp.Body.Close()
	w.Header().Set("Content-Type","image/jpeg")
	w.Header().Set("Cache-Control","public, max-age=86400")
	io.Copy(w,resp.Body)
}
func handleGetPlaylists(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
	db.RLock(); defer db.RUnlock()
	var list []map[string]interface{}
	for _,pl:=range db.Playlists{
		if pl.Owner==user||pl.Public||user==cfg.SubUser{
			cachedCount:=0
			for _,sid:=range pl.SongIDs{ if s,ok:=db.Songs[sid]; ok && s.TgFileID!=""{ cachedCount++ } }
			list=append(list,map[string]interface{}{"id":pl.ID,"name":pl.Name,"owner":pl.Owner,"songCount":len(pl.SongIDs),"created":pl.Created,"public":pl.Public,"comment":pl.Comment,"cachedCount":cachedCount})
		}
	}
	respond(w,r,map[string]interface{}{"playlists":map[string]interface{}{"playlist":list}})
}
func handleGetPlaylist(w http.ResponseWriter,r *http.Request){
	if ok,_:=checkAuth(r); !ok{ writeJSON(w,200,subFail("auth",40)); return }
	id:=r.URL.Query().Get("id")
	db.RLock(); pl,ok:=db.Playlists[id]; db.RUnlock()
	if !ok{ writeJSON(w,200,subFail("not found",70)); return }
	db.RLock()
	var songs []map[string]interface{}
	for _,sid:=range pl.SongIDs{ if s,ok:=db.Songs[sid]; ok{ songs=append(songs,songToSubsonic(s,r)) } }
	db.RUnlock()
	respond(w,r,map[string]interface{}{"playlist":map[string]interface{}{"id":pl.ID,"name":pl.Name,"owner":pl.Owner,"songCount":len(songs),"entry":songs,"created":pl.Created,"public":pl.Public,"comment":pl.Comment}})
}
func handleCreatePlaylist(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
	name:=r.URL.Query().Get("name"); if name==""{ name="New Playlist" }
	id:="pl_"+fmt.Sprintf("%d",time.Now().UnixNano())
	pl:=&Playlist{ID:id,Name:name,Owner:user,SongIDs:[]string{},Created:time.Now().Format(time.RFC3339)}
	db.Lock(); db.Playlists[id]=pl; db.Unlock()
	db.save()
	respond(w,r,map[string]interface{}{"playlist":map[string]interface{}{"id":pl.ID,"name":pl.Name}})
}
func handleUpdatePlaylist(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
	id:=r.URL.Query().Get("playlistId"); if id==""{ id=r.URL.Query().Get("id") }
	name:=r.URL.Query().Get("name")
	toAdd:=r.URL.Query()["songIdToAdd"]
	toRemoveIdxStr:=r.URL.Query()["songIndexToRemove"]
	db.Lock()
	pl,ok:=db.Playlists[id]
	if !ok{ db.Unlock(); writeJSON(w,200,subFail("not found",70)); return }
	if pl.Owner!=user && user!=cfg.SubUser{ db.Unlock(); writeJSON(w,200,subFail("not owner",50)); return }
	if name!=""{ pl.Name=name }
	for _,sid:=range toAdd{ if _,exists:=db.Songs[sid]; exists{ if !contains(pl.SongIDs,sid){ pl.SongIDs=append(pl.SongIDs,sid) } } }
	if len(toRemoveIdxStr)>0{
		var idxs []int
		for _,s:=range toRemoveIdxStr{ if i,err:=strconv.Atoi(s); err==nil{ idxs=append(idxs,i) } }
		for a:=0;a<len(idxs);a++{ for b:=a+1;b<len(idxs);b++{ if idxs[a]<idxs[b]{ idxs[a],idxs[b]=idxs[b],idxs[a] } } }
		for _,idx:=range idxs{ if idx>=0&&idx<len(pl.SongIDs){ pl.SongIDs=append(pl.SongIDs[:idx],pl.SongIDs[idx+1:]...) } }
	}
	db.Unlock()
	db.save(); go telegramUploadDB()
	respond(w,r,map[string]interface{}{})
}
func handleDeletePlaylist(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
	id:=r.URL.Query().Get("id")
	db.Lock()
	if pl,ok:=db.Playlists[id]; ok{ if pl.Owner==user||user==cfg.SubUser{ delete(db.Playlists,id) } }
	db.Unlock()
	db.save()
	respond(w,r,map[string]interface{}{})
}
func handleImportYT(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
	u:=r.URL.Query().Get("url"); if u==""{ u=r.URL.Query().Get("id") }; if u==""{ u=r.URL.Query().Get("playlistId") }
	pl,err:=importYoutubePlaylist(u,user)
	if err!=nil{ writeJSON(w,200,subFail(err.Error(),0)); return }
	respond(w,r,map[string]interface{}{"playlist":map[string]interface{}{"id":pl.ID,"name":pl.Name,"songCount":len(pl.SongIDs)}})
}
func handleCreateUser(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
	if user!=cfg.SubUser{ writeJSON(w,200,subFail("only admin",50)); return }
	newU:=r.URL.Query().Get("username"); newP:=r.URL.Query().Get("password")
	if newU==""||newP==""{ writeJSON(w,200,subFail("username password required",10)); return }
	muUsers.Lock(); usersMap[newU]=newP; muUsers.Unlock()
	db.Lock(); db.Users[newU]=newP; db.Unlock()
	db.save(); go telegramUploadDB()
	respond(w,r,map[string]interface{}{"user":map[string]interface{}{"username":newU}})
}
func handleGetUsers(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok||user!=cfg.SubUser{ writeJSON(w,200,subFail("admin only",50)); return }
	muUsers.RLock()
	var list []map[string]interface{}
	for u := range usersMap{ list=append(list,map[string]interface{}{"username":u,"adminRole":u==cfg.SubUser}) }
	muUsers.RUnlock()
	respond(w,r,map[string]interface{}{"users":map[string]interface{}{"user":list}})
}
func handleStar(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
	ids:=r.URL.Query()["id"]
	db.Lock()
	if _,ok:=db.Starred[user]; !ok{ db.Starred[user]=make(map[string]bool) }
	for _,id:=range ids{ db.Starred[user][id]=true }
	db.Unlock()
	db.save()
	respond(w,r,map[string]interface{}{})
}
func handleUnstar(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
	ids:=r.URL.Query()["id"]
	db.Lock()
	if m,ok:=db.Starred[user]; ok{
		for _,id:=range ids{ delete(m,id) }
	}
	db.Unlock()
	db.save()
	respond(w,r,map[string]interface{}{})
}
func handleGetStarred(w http.ResponseWriter,r *http.Request){
	ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
	db.RLock()
	starMap:=db.Starred[user]
	var songs []map[string]interface{}
	for sid := range starMap{
		if s,ok:=db.Songs[sid]; ok{
			songs=append(songs,songToSubsonic(s,r))
		}
	}
	db.RUnlock()
	respond(w,r,map[string]interface{}{"starred":map[string]interface{}{"song":songs}})
}
func handleGetStarred2(w http.ResponseWriter,r *http.Request){ handleGetStarred(w,r) }
func handleScrobble(w http.ResponseWriter,r *http.Request){
	id:=r.URL.Query().Get("id")
	if id!=""{
		db.Lock(); if s,ok:=db.Songs[id]; ok{ s.PlayCount++ }; db.Unlock()
	}
	respond(w,r,map[string]interface{}{})
}
func handleScanStatus(w http.ResponseWriter,r *http.Request){ respond(w,r,map[string]interface{}{"scanStatus":map[string]interface{}{"scanning":false,"count":len(db.Songs)}}) }

const adminHTML = `<!DOCTYPE html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Subsonic TG Cache v3.0</title><script src="https://cdn.tailwindcss.com"></script><style>.mono{font-family:ui-monospace,Menlo,monospace}</style></head><body class="bg-[#0a0a0a] text-zinc-100 min-h-screen"><div class="max-w-6xl mx-auto p-4">
<div class="border border-zinc-800 rounded-2xl p-4 bg-zinc-900/40 flex flex-wrap justify-between gap-3"><div><div class="text-xl font-bold">🎵 Subsonic v3.0 TG Cache</div><div class="text-xs text-zinc-500 mono">1st Play → TG Cache → Next Play TG Direct (No yt-dlp) • Album Art • Offline • Favourite</div></div><div class="flex gap-2"><input id="srv" placeholder="https://app.koyeb.app" class="bg-zinc-800 border border-zinc-700 rounded-lg px-3 py-2 text-sm w-56 mono"><input id="au" value="admin" class="bg-zinc-800 border border-zinc-700 rounded-lg px-3 py-2 text-sm w-20"><input id="ap" type="password" value="admin" class="bg-zinc-800 border border-zinc-700 rounded-lg px-3 py-2 text-sm w-20"><button onclick="saveCred()" class="bg-lime-400 text-black px-4 py-2 rounded-lg text-sm font-bold">Save</button></div></div>
<div class="grid grid-cols-2 md:grid-cols-4 gap-3 mt-4"><div class="bg-zinc-900 border border-zinc-800 rounded-xl p-4"><div class="text-xs text-zinc-500 mono">SONGS</div><div id="statSongs" class="text-2xl font-bold">-</div></div><div class="bg-zinc-900 border border-zinc-800 rounded-xl p-4"><div class="text-xs text-zinc-500 mono">TG CACHED</div><div id="statCached" class="text-2xl font-bold text-lime-400">-</div><div class="text-[10px] text-zinc-500">no yt-dlp next time</div></div><div class="bg-zinc-900 border border-zinc-800 rounded-xl p-4"><div class="text-xs text-zinc-500 mono">PLAYLISTS</div><div id="statPls" class="text-2xl font-bold">-</div></div><div class="bg-zinc-900 border border-zinc-800 rounded-xl p-4"><div class="text-xs text-zinc-500 mono">USERS</div><div id="statUsers" class="text-2xl font-bold">-</div></div></div>
<div class="mt-4 bg-zinc-900 border border-zinc-800 rounded-2xl p-5"><div class="font-semibold">▶️ How TG Cache Works (Tune jo bola)</div><div class="text-xs text-zinc-400 mt-2 leading-relaxed">1st play pe: <span class="text-white mono">/rest/stream.view?id=yt_xxx</span> → yt-dlp se direct URL → user ko stream + background me Telegram pe <span class="text-lime-300">sendAudio</span> se upload → file_id DB me save (TgFileID).<br>2nd play se: <span class="text-white mono">telegramGetFileUrl(TgFileID)</span> → TG file server se direct proxy, yt-dlp bilkul nahi chalega. X-Cache header: TG / MISS.<br>Isse Koyeb 512MB me bhi fast, har gaane pe yt-dlp nahi.</div><div class="mt-3 flex gap-2"><input id="yturl" placeholder="YT playlist URL PL..." class="flex-1 bg-zinc-800 border border-zinc-700 rounded-lg px-3 py-2.5 text-sm mono"><button onclick="importYT()" class="bg-white text-black px-4 py-2 rounded-lg text-sm font-bold">Import YT</button></div><div id="ytRes" class="mt-2 text-xs mono bg-black border border-zinc-800 rounded p-2 max-h-24 overflow-auto"></div></div>
<div class="mt-4 grid md:grid-cols-2 gap-4"><div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-5"><div class="flex justify-between"><div class="font-semibold">📀 Playlists (cached count)</div><button onclick="loadPlaylists()" class="bg-zinc-800 border border-zinc-700 px-3 py-1 rounded text-xs">Refresh</button></div><div id="pls" class="mt-3 space-y-2 max-h-96 overflow-auto"></div></div><div class="bg-zinc-900 border border-zinc-800 rounded-2xl p-5"><div class="flex justify-between"><div class="font-semibold">⭐ Favourites + Offline Download</div><button onclick="loadStarred()" class="bg-zinc-800 border border-zinc-700 px-3 py-1 rounded text-xs">Refresh</button></div><div id="starred" class="mt-3 space-y-2 max-h-96 overflow-auto"></div></div></div>
<div class="mt-4 bg-zinc-900 border border-zinc-800 rounded-2xl p-5"><div class="font-semibold">🔧 Tester</div><div class="flex flex-wrap gap-2 mt-3"><button onclick="test('ping')" class="bg-zinc-800 border border-zinc-700 px-3 py-2 rounded text-xs mono">ping</button><button onclick="test('playlists')" class="bg-zinc-800 border border-zinc-700 px-3 py-2 rounded text-xs mono">getPlaylists</button><button onclick="test('starred')" class="bg-zinc-800 border border-zinc-700 px-3 py-2 rounded text-xs mono">getStarred</button><button onclick="test('health')" class="bg-zinc-800 border border-zinc-700 px-3 py-2 rounded text-xs mono">/health</button></div><pre id="apiRes" class="mt-3 bg-black border border-zinc-800 rounded p-3 text-xs mono max-h-64 overflow-auto"></pre></div>
</div><script>
function getBase(){return (document.getElementById('srv').value||'').replace(/\/+$/,'')||location.origin}
function getCred(){return {u:document.getElementById('au').value||'admin',p:document.getElementById('ap').value||'admin'}}
function saveCred(){localStorage.setItem('sub_srv',document.getElementById('srv').value);localStorage.setItem('sub_u',document.getElementById('au').value);localStorage.setItem('sub_p',document.getElementById('ap').value);toast('Saved');loadStats()}
function toast(m){let d=document.createElement('div');d.textContent=m;d.className='fixed bottom-4 right-4 bg-zinc-800 border border-zinc-700 text-white px-4 py-2 rounded-lg text-sm';document.body.appendChild(d);setTimeout(()=>d.remove(),2000)}
async function loadStats(){try{let b=getBase();let r=await fetch(b+'/health');let j=await r.json();document.getElementById('statSongs').textContent=j.songs||0;document.getElementById('statPls').textContent=j.playlists||0;document.getElementById('statUsers').textContent=j.users||0;let cached=0;if(j.songs>0){try{let cr=await fetch(b+'/rest/search3.view?u='+encodeURIComponent(getCred().u)+'&p='+encodeURIComponent(getCred().p)+'&v=1.16.1&c=amcfy&f=json&query=a');let cj=await cr.json();let songs=cj['subsonic-response']?.searchResult3?.song||[];cached=j.cached||0}catch{}}document.getElementById('statCached').textContent=j.cached||'?';}catch{}}
async function importYT(){let url=document.getElementById('yturl').value.trim();if(!url)return toast('Paste YT URL');let {u,p}=getCred();let base=getBase();document.getElementById('ytRes').textContent='Importing as '+u+'...';try{let api=base+'/rest/importYoutubePlaylist.view?u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p)+'&v=1.16.1&c=amcfy&f=json&url='+encodeURIComponent(url);let r=await fetch(api);let j=await r.json();document.getElementById('ytRes').textContent=JSON.stringify(j,null,2);toast('Imported');loadStats();loadPlaylists();}catch(e){document.getElementById('ytRes').textContent='Error:'+e}}
async function loadPlaylists(){let {u,p}=getCred();let base=getBase();try{let api=base+'/rest/getPlaylists.view?u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p)+'&v=1.16.1&c=amcfy&f=json';let r=await fetch(api);let j=await r.json();let list=j['subsonic-response']?.playlists?.playlist||[];if(!Array.isArray(list))list=[list];let html=list.map(pl=>'<div class="bg-black border border-zinc-800 rounded-lg p-3"><div class="flex justify-between"><div><div class="font-medium">'+pl.name+'</div><div class="text-xs text-zinc-500 mono">'+pl.owner+' • '+pl.songCount+' songs • cached '+ (pl.cachedCount||0)+'</div></div><button onclick="viewPl(\''+pl.id+'\')" class="bg-zinc-800 px-2 py-1 rounded text-xs">View</button></div></div>').join('');document.getElementById('pls').innerHTML=html||'No playlists';}catch(e){document.getElementById('pls').textContent='Error:'+e}}
async function viewPl(id){let {u,p}=getCred();let base=getBase();let api=base+'/rest/getPlaylist.view?u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p)+'&v=1.16.1&c=amcfy&f=json&id='+id;let r=await fetch(api);let j=await r.json();document.getElementById('apiRes').textContent=JSON.stringify(j,null,2)}
async function loadStarred(){let {u,p}=getCred();let base=getBase();try{let api=base+'/rest/getStarred.view?u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p)+'&v=1.16.1&c=amcfy&f=json';let r=await fetch(api);let j=await r.json();let songs=j['subsonic-response']?.starred?.song||[];if(!Array.isArray(songs))songs=[songs];let html=songs.map(s=>'<div class="bg-black border border-zinc-800 rounded-lg p-2 flex gap-2 items-center"><img src="'+base+'/rest/getCoverArt.view?id='+s.id+'&u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p)+'" class="w-10 h-10 rounded"><div class="flex-1"><div class="text-sm">'+s.title+'</div><div class="text-xs text-zinc-500">'+s.artist+(s.cached?' • TG cached':'')+'</div></div><a href="'+base+'/rest/download.view?id='+s.id+'&u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p)+'" class="bg-zinc-800 px-2 py-1 rounded text-xs">⬇️</a></div>').join('');document.getElementById('starred').innerHTML=html||'No fav';}catch(e){document.getElementById('starred').textContent='Error:'+e}}
async function test(w){let {u,p}=getCred();let base=getBase();let url='';if(w==='ping')url=base+'/rest/ping.view?u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p)+'&f=json';if(w==='playlists')url=base+'/rest/getPlaylists.view?u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p)+'&f=json';if(w==='starred')url=base+'/rest/getStarred.view?u='+encodeURIComponent(u)+'&p='+encodeURIComponent(p)+'&f=json';if(w==='health')url=base+'/health';let r=await fetch(url);let t=await r.text();document.getElementById('apiRes').textContent=t.slice(0,4000)}
window.addEventListener('DOMContentLoaded',()=>{let s=localStorage.getItem('sub_srv');if(s)document.getElementById('srv').value=s;let su=localStorage.getItem('sub_u');if(su)document.getElementById('au').value=su;let sp=localStorage.getItem('sub_p');if(sp)document.getElementById('ap').value=sp;loadStats();loadPlaylists();loadStarred();});
</script></body></html>`

func main(){
	cfg=loadConfig()
	db.load()
	if len(db.Songs)==0&&cfg.TelegramFileID!=""{
		telegramDownloadDB()
		db.load()
	}
	os.MkdirAll(filepath.Dir(cfg.DbPath),0755)
	initUsers()
	mux:=http.NewServeMux()
	mux.HandleFunc("/rest/ping.view",handlePing)
	mux.HandleFunc("/rest/getLicense.view",handleLicense)
	mux.HandleFunc("/rest/getMusicFolders.view",handleMusicFolders)
	mux.HandleFunc("/rest/getUser.view",handleGetUser)
	mux.HandleFunc("/rest/getArtists.view",handleGetArtists)
	mux.HandleFunc("/rest/getArtist.view",handleGetArtist)
	mux.HandleFunc("/rest/getAlbum.view",handleGetAlbum)
	mux.HandleFunc("/rest/getSong.view",handleGetSong)
	mux.HandleFunc("/rest/search3.view",handleSearch3)
	mux.HandleFunc("/rest/getAlbumList2.view",handleAlbumList2)
	mux.HandleFunc("/rest/getRandomSongs.view",handleRandomSongs)
	mux.HandleFunc("/rest/stream.view",handleStream)
	mux.HandleFunc("/rest/download.view",handleDownload)
	mux.HandleFunc("/rest/getCoverArt.view",handleCoverArt)
	mux.HandleFunc("/rest/getPlaylists.view",handleGetPlaylists)
	mux.HandleFunc("/rest/getPlaylist.view",handleGetPlaylist)
	mux.HandleFunc("/rest/createPlaylist.view",handleCreatePlaylist)
	mux.HandleFunc("/rest/deletePlaylist.view",handleDeletePlaylist)
	mux.HandleFunc("/rest/updatePlaylist.view",handleUpdatePlaylist)
	mux.HandleFunc("/rest/importYoutubePlaylist.view",handleImportYT)
	mux.HandleFunc("/rest/importPlaylist.view",handleImportYT)
	mux.HandleFunc("/rest/createUser.view",handleCreateUser)
	mux.HandleFunc("/rest/getUsers.view",handleGetUsers)
	mux.HandleFunc("/rest/star.view",handleStar)
	mux.HandleFunc("/rest/unstar.view",handleUnstar)
	mux.HandleFunc("/rest/getStarred.view",handleGetStarred)
	mux.HandleFunc("/rest/getStarred2.view",handleGetStarred2)
	mux.HandleFunc("/rest/scrobble.view",handleScrobble)
	mux.HandleFunc("/rest/getScanStatus.view",handleScanStatus)
	mux.HandleFunc("/health",func(w http.ResponseWriter,r *http.Request){
		cached:=0
		db.RLock()
		for _,s:=range db.Songs{ if s.TgFileID!=""{ cached++ } }
		db.RUnlock()
		writeJSON(w,200,map[string]interface{}{"status":"ok","songs":len(db.Songs),"playlists":len(db.Playlists),"users":len(usersMap),"cached":cached,"artists":len(db.Artists),"albums":len(db.Albums)})
	})
	mux.HandleFunc("/admin/cache-stats",func(w http.ResponseWriter,r *http.Request){
		ok,user:=checkAuth(r); if !ok{ writeJSON(w,200,subFail("auth",40)); return }
		cached:=0; var list []map[string]interface{}
		db.RLock()
		for _,s:=range db.Songs{
			if s.TgFileID!=""{
				cached++
				list=append(list,map[string]interface{}{"id":s.ID,"title":s.Title,"artist":s.Artist,"cachedAt":s.CachedAt})
			}
		}
		db.RUnlock()
		if len(list)>50{ list=list[:50] }
		respond(w,r,map[string]interface{}{"cachedCount":cached,"songs":list})
	})
	mux.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){
		if r.URL.Path!="/"{ http.NotFound(w,r); return }
		w.Header().Set("Content-Type","text/html")
		fmt.Fprint(w,adminHTML)
	})
	go func(){
		ticker:=time.NewTicker(5*time.Minute)
		for range ticker.C{ db.save(); go telegramUploadDB() }
	}()
	port:=cfg.Port
	if !strings.HasPrefix(port,":"){ port=":"+port }
	log.Printf("Starting FINAL TG-CACHE v3.0 on %s users=%d songs=%d cached=%d",port,len(usersMap),len(db.Songs),countCached())
	log.Fatal(http.ListenAndServe(port,mux))
}
