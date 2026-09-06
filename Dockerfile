FROM golang:1.22-alpine AS builder
WORKDIR /app

# pehle go.mod download
COPY go.mod ./
RUN go mod download

# ab saare files copy
COPY . .

# static binary build - Koyeb ke liye best
RUN CGO_ENABLED=0 GOOS=linux go mod tidy && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o extractor .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/extractor .
EXPOSE 8000
CMD ["./extractor"]
