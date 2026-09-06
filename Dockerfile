FROM golang:1.21-bullseye

RUN apt-get update && apt-get install -y python3 python3-pip ffmpeg curl && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && apt-get install -y nodejs

RUN pip3 install --no-cache-dir -U yt-dlp

WORKDIR /app

COPY main.go ./

RUN GO111MODULE=off go build -o server main.go

ENV PORT=8000
EXPOSE 8000
CMD ["./server"]