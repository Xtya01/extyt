# v6 - Telegram JSON DB (512 MB Nano friendly)

Isme 2 cheez Telegram pe hai:
1. Gaane (audio files)
2. db.json (index)

Koyeb free me disk udd jata hai, isliye DB bhi Telegram pe backup hota hai.

### ENV vars Koyeb pe:
BOT_TOKEN=123:AAHxxx (BotFather se)
CHANNEL_ID=-100xxxx (jaha gaane save honge)
DB_JSON_FILE_ID= (optional) pehle backup ka file_id, restore ke liye

### Flow:
- /play?url=YT_URL -> pehli baar YT se download -> Telegram pe upload -> db.json me entry + db.json ka backup Telegram pe
- Dusri baar -> db.json me file_id mil gaya -> direct Telegram se redirect, YT touch nahi, RAM 25 MB

- /list -> saare gaane ka JSON index dekho
- /db -> db.json download karo
- /health -> kitne gaane indexed hai

### Telegram se DB restore kaise hota hai?
Koyeb sleep hoke uthega to /tmp/db.json gayab. Agar DB_JSON_FILE_ID ENV me hai to app boot pe Telegram se db.json download karke restore kar lega. Naya backup banta hai to log me file_id dikhega, usko ENV me update kar do.

### Cookies:
cookies.txt ko base64 karke YT_COOKIES_B64 ENV me daalo, ya file ke roop me repo me daalo.
