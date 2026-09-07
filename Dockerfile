FROM golang:1.21-bookworm
RUN apt-get update && apt-get install -y ffmpeg curl && rm -rf /var/lib/apt/lists/*
RUN pip3 install --break-system-packages -U yt-dlp 2>/dev/null || pip3 install -U yt-dlp
WORKDIR /app
COPY go.mod./
COPY main.go./
RUN go mod tidy && go build -o server main.go
ENV PORT=8000
EXPOSE 8000
CMD ["./server"]