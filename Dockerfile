# Stage 1: Static binary build
FROM golang:1.22-bookworm AS builder
WORKDIR /app
COPY main.go ./
RUN GO111MODULE=off CGO_ENABLED=0 go build -ldflags="-s -w" -o server main.go

# Stage 2: Minimal runtime environment for Koyeb Free Tier
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    curl \
    ca-certificates \
    python3 \
    nodejs \
    && rm -rf /var/lib/apt/lists/*

# Standalone yt-dlp binary
RUN curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp && chmod a+rx /usr/local/bin/yt-dlp

WORKDIR /app
COPY --from=builder /app/server /app/server

ENV PORT=8000
EXPOSE 8000

CMD ["./server"]
