FROM golang:1.21-bookworm

RUN apt-get update && apt-get install -y ffmpeg curl && rm -rf /var/lib/apt/lists/*

RUN curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp && chmod +x /usr/local/bin/yt-dlp && yt-dlp --version

WORKDIR /app
COPY main.go ./

RUN GO111MODULE=off go build -o server main.go

ENV PORT=8000
EXPOSE 8000
CMD ["./server"]