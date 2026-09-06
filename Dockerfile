FROM golang:1.21-bullseye

RUN apt-get update && apt-get install -y python3 python3-pip ffmpeg curl nodejs npm && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://deno.land/install.sh | sh
ENV DENO_INSTALL="/root/.deno"
ENV PATH="$DENO_INSTALL/bin:/usr/local/go/bin:$PATH"

RUN pip3 install --no-cache-dir -U yt-dlp

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o server main.go

ENV PORT=8000
EXPOSE 8000
CMD ["./server"]