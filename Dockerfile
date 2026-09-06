FROM golang:1.22-alpine
RUN apk add --no-cache python3 py3-pip ffmpeg ca-certificates
RUN pip3 install --no-cache-dir --break-system-packages -U yt-dlp
WORKDIR /app
COPY go.mod ./
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -ldflags="-s -w" -o extractor .
EXPOSE 8000
CMD ["./extractor"]
