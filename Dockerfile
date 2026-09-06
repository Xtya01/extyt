FROM golang:1.21-bullseye

RUN apt-get update && apt-get install -y python3 python3-pip ffmpeg curl unzip && rm -rf /var/lib/apt/lists/*

# Node 20 - n challenge ke liye chahiye
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && apt-get install -y nodejs

# Deno
RUN curl -fsSL https://deno.land/install.sh | sh || true
ENV DENO_INSTALL="/root/.deno"
ENV PATH="$DENO_INSTALL/bin:/usr/local/go/bin:$PATH"

RUN pip3 install -U yt-dlp

WORKDIR /app
COPY . .
RUN go mod tidy || true
RUN go build -o server .

ENV PORT=8000
EXPOSE 8000
CMD ["./server"]