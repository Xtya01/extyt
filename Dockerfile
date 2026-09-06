FROM python:3.11-slim

RUN apt-get update && apt-get install -y ffmpeg curl nodejs npm && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://deno.land/install.sh | sh
ENV DENO_INSTALL="/root/.deno"
ENV PATH="$DENO_INSTALL/bin:$PATH"

RUN node --version && deno --version

WORKDIR /app
COPY requirements.txt.
RUN pip install --no-cache-dir -U pip && pip install --no-cache-dir -r requirements.txt && pip install --no-cache-dir -U yt-dlp
COPY..
ENV PORT=8000
EXPOSE 8000
CMD ["python", "main.py"]