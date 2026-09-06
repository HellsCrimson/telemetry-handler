# GPU voice server for telemetry-handler (STT + TTS on the V100).
#
# cuDNN 9 is required by CTranslate2 >= 4.5 (faster-whisper's backend), which is
# why this starts from the -cudnn- runtime image rather than plain CUDA.
FROM nvidia/cuda:12.4.1-cudnn-runtime-ubuntu22.04

ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    HF_HOME=/cache/huggingface

# espeak-ng is misaki's fallback grapheme-to-phoneme backend for Kokoro.
RUN apt-get update && apt-get install -y --no-install-recommends \
        python3 python3-pip python3-venv \
        espeak-ng libsndfile1 curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# sm_70 (Volta / V100) is covered by the standard cu124 wheels.
RUN python3 -m pip install --upgrade pip \
 && python3 -m pip install torch==2.6.0 --index-url https://download.pytorch.org/whl/cu124

COPY requirements.txt /app/requirements.txt
RUN python3 -m pip install -r /app/requirements.txt

COPY server.py /app/server.py
WORKDIR /app

EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=5s --start-period=300s --retries=3 \
    CMD curl -fsS http://localhost:8000/healthz || exit 1

CMD ["python3", "-m", "uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8000", "--ws-max-size", "16777216"]
