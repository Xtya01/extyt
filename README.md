# Go Subsonic Jio+YT - Final v2.1
Multi-user, YT First Play, Album Art, Offline Download, Favourite

## Env Vars for Koyeb
PORT=8000
SUBSONIC_USER=admin
SUBSONIC_PASSWORD=admin123
SUBSONIC_USERS=admin:admin123,rahul:rahul123,anil:anil123
YT_API_KEY=AIzaSy...
YT_COOKIES= (optional netscape cookies)
TELEGRAM_BOT_TOKEN=...
TELEGRAM_CHAT_ID=...
TELEGRAM_DB_FILE_ID=...
JIOSAAVN_API_URL=https://jiosaavn-api-three-ashy.vercel.app
DB_PATH=/app/data/db.json

## Features
- YT First Play: stream.view -> yt-dlp -> m4a cache 30min
- Album Art: getCoverArt.view?id=xxx -> YT thumbnail proxy + cache header
- Offline Download: download.view?id=xxx returns attachment
- Favourite: star.view?id=xxx & unstar & getStarred.view per user
- Self Playlist: owner field, getPlaylists filters owner==user || public || admin sees all
- Admin: createUser.view?username=&password= , getUsers.view (admin only)
- Import YT: importYoutubePlaylist.view?url=PL...
- Telegram DB backup every 5 min

## AmcFy Setup
Server: https://your-app.koyeb.app/rest
User: rahul
Pass: rahul123
Type: Subsonic

## Important Endpoints
/rest/ping.view?u=&p=&f=json
/rest/search3.view?query=arijit&f=json
/rest/getPlaylists.view
/rest/getPlaylist.view?id=pl_...
/rest/createPlaylist.view?name=MyFav
/rest/updatePlaylist.view?playlistId=pl_...&songIdToAdd=yt_xxx
/rest/deletePlaylist.view?id=pl_...
/rest/star.view?id=yt_xxx
/rest/unstar.view?id=yt_xxx
/rest/getStarred.view
/rest/stream.view?id=yt_xxx (YT First)
/rest/download.view?id=yt_xxx (offline)
/rest/getCoverArt.view?id=yt_xxx (album art)
/rest/scrobble.view?id=xxx (playCount)
