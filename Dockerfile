FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod tidy
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o server .

FROM alpine:latest
RUN apk add --no-cache python3 py3-pip ffmpeg ca-certificates curl && \
    pip3 install --no-cache-dir yt-dlp --break-system-packages && \
    yt-dlp --version
WORKDIR /app
COPY --from=builder /app/server .
RUN mkdir -p /tmp /app/data
ENV PORT=8000
ENV JIOSAAVN_API_URL=https://jiosaavn-api-three-ashy.vercel.app
ENV DB_PATH=/app/data/db.json
EXPOSE 8000
CMD ["./server"]