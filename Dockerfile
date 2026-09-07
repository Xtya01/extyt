FROM golang:1.21-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg curl ca-certificates python3 python3-pip unzip \
    && rm -rf /var/lib/apt/lists/*

# yt-dlp + ejs support
RUN pip3 install --break-system-packages -U "yt-dlp[default]"

# Deno (recommended JS runtime)
RUN curl -fsSL https://deno.land/install.sh | DENO_INSTALL=/usr/local sh
ENV PATH="/usr/local/bin:$PATH"

# Node (fallback)
RUN apt-get update && apt-get install -y --no-install-recommends nodejs npm \
    && rm -rf /var/lib/apt/lists/* \
    && ln -sf /usr/bin/nodejs /usr/bin/node || true

RUN node --version && deno --version && yt-dlp --version

WORKDIR /app
COPY main.go ./
RUN GO111MODULE=off go build -o server main.go

ENV PORT=8000
EXPOSE 8000
CMD ["./server"]