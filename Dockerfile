FROM golang:1.22-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o server .

FROM debian:bookworm-slim
RUN apt-get update &&     apt-get install -y --no-install-recommends ffmpeg python3 python3-pip curl ca-certificates &&     pip3 install --break-system-packages -U yt-dlp &&     yt-dlp --version &&     apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/server .
ENV PORT=8000
EXPOSE 8000
CMD ["./server"]
