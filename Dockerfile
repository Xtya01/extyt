FROM golang:1.21-bookworm

RUN apt-get update && apt-get install -y python3-pip ffmpeg && rm -rf /var/lib/apt/lists/*

RUN pip3 install --no-cache-dir -U yt-dlp

WORKDIR /app
COPY main.go ./

RUN go build -o server main.go

ENV PORT=8000
EXPOSE 8000
CMD ["./server"]