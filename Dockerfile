FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY main.go ./
RUN go mod tidy && go build -ldflags="-s -w" -o extractor .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/extractor .
RUN chmod +x ./extractor
CMD ["./extractor"]
