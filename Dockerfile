FROM golang:1.22-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum./
RUN go mod download
COPY..
RUN CGO_ENABLED=0 go build -o server.

FROM debian:bookworm-slim
# ffmpeg + python + yt-dlp ek sath
RUN apt-get update && \
    apt-get install -y ffmpeg python3 python3-pip curl && \
    pip3 install --break-system-packages yt-dlp && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/server.
# Koyeb PORT env deta hai
CMD ["./server"]