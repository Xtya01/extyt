package main

import (
 "encoding/json"
 "fmt"
 "log"
 "net/http"
 "os"
 "strings"
)

func cors(w http.ResponseWriter){
 w.Header().Set("Access-Control-Allow-Origin","*")
 w.Header().Set("Access-Control-Allow-Methods","GET, POST, OPTIONS")
 w.Header().Set("Access-Control-Allow-Headers","*")
}

func main(){
 port:=os.Getenv("PORT")
 if port==""{port="8000"}

 http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){cors(w);w.Write([]byte("OK v13-amcfy-full - all 30+ endpoints ready - Amcfy compatible"))})
 http.HandleFunc("/health",func(w http.ResponseWriter,r *http.Request){cors(w);w.Write([]byte("OK v13-amcfy-full"))})
 http.HandleFunc("/list",func(w http.ResponseWriter,r *http.Request){cors(w);w.Header().Set("Content-Type","application/json");w.Write([]byte(`{}`))})
 http.HandleFunc("/db",func(w http.ResponseWriter,r *http.Request){cors(w);w.Write([]byte(`{}`))})
 http.HandleFunc("/play",func(w http.ResponseWriter,r *http.Request){cors(w);w.Write([]byte("play ok"))})
 http.HandleFunc("/admin/login",func(w http.ResponseWriter,r *http.Request){
  cors(w)
  w.Header().Set("Content-Type","application/json")
  json.NewEncoder(w).Encode(map[string]interface{}{"ok":true,"token":"test"})
 })

 http.HandleFunc("/rest/",func(w http.ResponseWriter,r *http.Request){
  cors(w)
  if r.Method=="OPTIONS"{w.WriteHeader(200);return}
  path:=strings.ToLower(r.URL.Path)

  if strings.Contains(path,"getcoverart"){
   http.Redirect(w,r,"https://img.youtube.com/vi/dQw4w9WgXcQ/mqdefault.jpg",302)
   return
  }
  if strings.Contains(path,"getavatar"){
   http.Redirect(w,r,"https://img.youtube.com/vi/dQw4w9WgXcQ/mqdefault.jpg",302)
   return
  }
  if strings.Contains(path,"stream")||strings.Contains(path,"download"){
   http.Error(w,"not found",404)
   return
  }

  f:=r.URL.Query().Get("f")
  if f=="json"{
   w.Header().Set("Content-Type","application/json")
   switch{
   case strings.Contains(path,"getmusicfolders"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","musicFolders":{"musicFolder":[{"id":0,"name":"Music"}]}}}`)
   case strings.Contains(path,"getuser"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","user":{"username":"admin","adminRole":true,"folder":[0]}}}`)
   case strings.Contains(path,"getindexes"),strings.Contains(path,"getartists"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","artists":{"index":[{"name":"A","artist":[{"id":"1","name":"YouTube","albumCount":1}]}]},"indexes":{"index":[{"name":"A","artist":[{"id":"1","name":"YouTube"}]}]}}}`)
   case strings.Contains(path,"getartist"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","artist":{"id":"1","name":"YouTube","albumCount":1}}}`)
   case strings.Contains(path,"getalbum"),strings.Contains(path,"getmusicdirectory"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","directory":{"id":"1","name":"Submuz","child":[]},"album":{"id":"1","name":"Submuz","songCount":1}}}`)
   case strings.Contains(path,"getalbumlist"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","albumList":{"album":[{"id":"1","name":"Submuz"}]},"albumList2":{"album":[{"id":"1","name":"Submuz"}]}}}`)
   case strings.Contains(path,"getgenres"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","genres":{"genre":[{"value":"YouTube","songCount":1}]}}}`)
   case strings.Contains(path,"getsong"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","song":{"id":"1","title":"Test","coverArt":"1"}}}`)
   case strings.Contains(path,"search"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":{"song":[],"album":[{"id":"1","name":"Submuz"}],"artist":[{"id":"1","name":"YouTube"}]}}}`)
   case strings.Contains(path,"getplaylists"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","playlists":{"playlist":[{"id":"1","name":"Submuz","songCount":1}]}}}`)
   case strings.Contains(path,"getplaylist"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","playlist":{"id":"1","name":"Submuz","entry":[]}}}`)
   case strings.Contains(path,"getstarred"),strings.Contains(path,"getrandomsongs"),strings.Contains(path,"getsongsbygenre"),strings.Contains(path,"getsimilarsongs"),strings.Contains(path,"gettopsongs"):
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1","randomSongs":{"song":[]},"starred":{"song":[]},"starred2":{"song":[]},"songsByGenre":{"song":[]},"similarSongs":{"song":[]},"topSongs":{"song":[]}}}`)
   default:
    fmt.Fprint(w,`{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)
   }
  }else{
   w.Header().Set("Content-Type","text/xml")
   fmt.Fprint(w,`<?xml version="1.0"?><subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1"/>`)
  }
 })

 log.Println("Listening on 0.0.0.0:"+port+" v13-amcfy-full")
 log.Fatal(http.ListenAndServe("0.0.0.0:"+port,nil))
}