FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o server .

FROM alpine:3.19
RUN apk add --no-cache ffmpeg python3 py3-pip
RUN pip3 install --break-system-packages --no-cache-dir -U yt-dlp
WORKDIR /app
COPY --from=builder /app/server .
COPY cookies.txt* ./
ENV PORT=8000
EXPOSE 8000
CMD ["./server"]
