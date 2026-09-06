FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go mod tidy && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o extractor .

FROM python:3.11-alpine
RUN apk add --no-cache ca-certificates ffmpeg
RUN pip install --no-cache-dir -U yt-dlp
WORKDIR /app
COPY --from=builder /app/extractor .
EXPOSE 8000
CMD ["./extractor"]
