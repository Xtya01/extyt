FROM golang:1.22-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    curl \
    ca-certificates \
    python3 \
    python3-pip \
    unzip \
    nodejs \
    npm \
    && rm -rf /var/lib/apt/lists/*

RUN pip3 install --break-system-packages -U "yt-dlp[default]" || \
    (curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp && chmod a+rx /usr/local/bin/yt-dlp)

RUN curl -fsSL https://deno.land/install.sh | DENO_INSTALL=/usr/local sh
ENV PATH="/usr/local/bin:$PATH"

RUN ln -sf /usr/bin/nodejs /usr/bin/node || true

RUN node --version && deno --version && (yt-dlp --version || true)

WORKDIR /app
COPY main.go ./
RUN GO111MODULE=off go build -o server main.go

ENV PORT=8000
EXPOSE 8000

CMD ["./server"]