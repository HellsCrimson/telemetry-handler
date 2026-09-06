#!/usr/bin/env python3
"""GPU voice server for telemetry-handler.

Runs on the box with the V100 and serves the two halves of the push-to-talk
assistant over WebSockets, with both models resident on the GPU:

  * STT — faster-whisper (CTranslate2). The client streams raw PCM *while the
    driver holds the trigger*, so by the time the button is released the audio
    is already on this side and only the decode remains (~100 ms for a short
    pit call on a V100 with distil-large-v3).
  * TTS — Kokoro on CUDA. Audio is streamed back sentence by sentence as PCM,
    so playback starts on the first chunk instead of after the whole utterance.

Wire format (both sockets are long-lived and reused for many utterances):

  WS /v1/stt
    → {"type":"start","language":"en","sample_rate":16000}
    → <binary> PCM s16le mono frames, as recorded
    → {"type":"eos"}
    ← {"type":"partial","text":"..."}       (only when VOICE_STT_PARTIALS=1)
    ← {"type":"final","text":"..."}
    ← {"type":"error","message":"..."}

  WS /v1/tts
    → {"type":"speak","text":"...","voice":"af_sarah","speed":1.0}
    → {"type":"cancel"}                     (barge-in: drop the current utterance)
    ← {"type":"begin","sample_rate":24000,"channels":1,"format":"s16le"}
    ← <binary> PCM s16le mono frames
    ← {"type":"end"} | {"type":"error","message":"..."}

  GET /healthz → readiness + the resolved model/device configuration.

Everything is configured by environment variable (see compose.yaml); there are
no command-line flags.
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import threading
import time
from contextlib import asynccontextmanager
from typing import Any, AsyncIterator, Iterator

import numpy as np
from fastapi import FastAPI, WebSocket, WebSocketDisconnect
from fastapi.responses import JSONResponse

log = logging.getLogger("voiced")

# --- Configuration ----------------------------------------------------------


def _env(name: str, default: str) -> str:
    return os.environ.get(name, default).strip()


def _env_int(name: str, default: int) -> int:
    try:
        return int(_env(name, str(default)) or default)
    except ValueError:
        return default


def _env_float(name: str, default: float) -> float:
    try:
        return float(_env(name, str(default)) or default)
    except ValueError:
        return default


def _env_bool(name: str, default: bool = False) -> bool:
    return _env(name, "1" if default else "0").lower() in ("1", "true", "yes", "on")


STT_MODEL = _env("VOICE_STT_MODEL", "distil-large-v3")
STT_DEVICE = _env("VOICE_STT_DEVICE", "cuda")
STT_COMPUTE = _env("VOICE_STT_COMPUTE", "float16")
STT_LANGUAGE = _env("VOICE_STT_LANGUAGE", "en")
STT_BEAM = _env_int("VOICE_STT_BEAM", 1)
# Partial transcripts give the driver an on-overlay echo while still speaking,
# at the cost of extra GPU passes that compete with the final decode. Off by
# default: the final decode is what the confirmation flow waits on.
STT_PARTIALS = _env_bool("VOICE_STT_PARTIALS", False)
STT_PARTIAL_MS = _env_int("VOICE_STT_PARTIAL_MS", 700)
# Anything shorter than this is a mis-press, not speech.
STT_MIN_MS = _env_int("VOICE_STT_MIN_MS", 200)
STT_SAMPLE_RATE = _env_int("VOICE_STT_SAMPLE_RATE", 16000)

TTS_DEVICE = _env("VOICE_TTS_DEVICE", "cuda")
TTS_LANG = _env("VOICE_TTS_LANG", "a")  # kokoro lang_code: a = American English
TTS_VOICE = _env("VOICE_TTS_VOICE", "af_sarah")
TTS_SPEED = _env_float("VOICE_TTS_SPEED", 1.0)
TTS_REPO = _env("VOICE_TTS_REPO", "hexgrad/Kokoro-82M")
TTS_SAMPLE_RATE = 24000  # Kokoro's fixed output rate

# Kokoro pads roughly half a second of silence before and after every phrase.
# The bytes reach the client in ~70 ms, so that padding *is* the latency the
# driver hears — trimming it is worth more than any other tuning here.
TTS_TRIM_SILENCE = _env_bool("VOICE_TTS_TRIM_SILENCE", True)
# Amplitude below which a sample counts as silence (~1.5% of full scale).
TTS_SILENCE_LEVEL = _env_int("VOICE_TTS_SILENCE_LEVEL", 500)
# Silence kept either side of the speech, so nothing starts or ends clipped.
TTS_KEEP_PAD_MS = _env_int("VOICE_TTS_KEEP_PAD_MS", 30)
# Silence inserted between sentence chunks, since trimming removes the natural
# pause that would otherwise separate them.
TTS_GAP_MS = _env_int("VOICE_TTS_GAP_MS", 90)

# Optional shared secret, checked as ?token=… on both sockets. Empty disables the
# check (fine on a trusted LAN).
TOKEN = _env("VOICE_TOKEN", "")

# Chunks larger than this are split before sending so playback can start sooner.
MAX_FRAME_SAMPLES = 4800  # 200 ms at 24 kHz


# --- Speech to text ---------------------------------------------------------


class STT:
    """faster-whisper, resident on the GPU.

    Decodes are serialized by a lock: the model is not thread-safe, and letting a
    partial and a final decode overlap would only slow the final one down.
    """

    def __init__(self) -> None:
        from faster_whisper import WhisperModel

        log.info("stt: loading %s (%s/%s)", STT_MODEL, STT_DEVICE, STT_COMPUTE)
        self.model = WhisperModel(STT_MODEL, device=STT_DEVICE, compute_type=STT_COMPUTE)
        self.lock = asyncio.Lock()

    async def transcribe(self, audio: np.ndarray, language: str) -> str:
        async with self.lock:
            return await asyncio.to_thread(self._transcribe, audio, language)

    def _transcribe(self, audio: np.ndarray, language: str) -> str:
        segments, _ = self.model.transcribe(
            audio,
            language=language or None,
            beam_size=STT_BEAM,
            vad_filter=True,
            condition_on_previous_text=False,
            without_timestamps=True,
        )
        return " ".join(s.text.strip() for s in segments).strip()

    def warm(self) -> None:
        self._transcribe(np.zeros(STT_SAMPLE_RATE, dtype=np.float32), STT_LANGUAGE)


def pcm_to_float(buf: bytes) -> np.ndarray:
    """Convert little-endian s16 PCM to the float32 [-1,1] whisper expects."""
    if len(buf) % 2:
        buf = buf[:-1]
    return np.frombuffer(buf, dtype="<i2").astype(np.float32) / 32768.0


# --- Text to speech ---------------------------------------------------------


class TTS:
    """Kokoro on CUDA, yielding int16 PCM chunks as they are synthesized."""

    def __init__(self) -> None:
        from kokoro import KPipeline

        log.info("tts: loading kokoro %s (%s)", TTS_REPO, TTS_DEVICE)
        self.pipeline = KPipeline(lang_code=TTS_LANG, repo_id=TTS_REPO, device=TTS_DEVICE)
        self.lock = threading.Lock()

    def stream(self, text: str, voice: str, speed: float) -> Iterator[np.ndarray]:
        # Splitting on sentence boundaries (rather than kokoro's default of blank
        # lines) is what makes a multi-sentence answer start playing early.
        with self.lock:
            first = True
            for item in self.pipeline(
                text,
                voice=voice or TTS_VOICE,
                speed=speed or TTS_SPEED,
                split_pattern=r"(?<=[.!?])\s+|\n+",
            ):
                audio = getattr(item, "audio", None)
                if audio is None:  # older kokoro yields a (graphemes, phonemes, audio) tuple
                    audio = item[2]
                if audio is None:
                    continue
                pcm = _to_int16(audio)
                if TTS_TRIM_SILENCE:
                    pcm = _trim_silence(pcm)
                if pcm.size == 0:
                    continue
                if not first:
                    # Restore the pause trimming took out from between sentences.
                    pcm = np.concatenate([_silence(TTS_GAP_MS), pcm])
                first = False
                yield from _frames(pcm)

    def warm(self) -> None:
        for _ in self.stream("Radio check.", TTS_VOICE, TTS_SPEED):
            pass


def _to_int16(audio: Any) -> np.ndarray:
    """Normalize a kokoro chunk (torch tensor or array) to int16 PCM."""
    if hasattr(audio, "detach"):  # torch tensor
        audio = audio.detach().cpu().numpy()
    samples = np.asarray(audio, dtype=np.float32).reshape(-1)
    return (np.clip(samples, -1.0, 1.0) * 32767.0).astype("<i2")


def _trim_silence(pcm: np.ndarray) -> np.ndarray:
    """Drop the near-silent head and tail, keeping a short pad either side."""
    loud = np.flatnonzero(np.abs(pcm.astype(np.int32)) >= TTS_SILENCE_LEVEL)
    if loud.size == 0:
        return pcm[:0]
    pad = TTS_SAMPLE_RATE * TTS_KEEP_PAD_MS // 1000
    return pcm[max(0, int(loud[0]) - pad) : min(len(pcm), int(loud[-1]) + 1 + pad)]


def _silence(ms: int) -> np.ndarray:
    return np.zeros(TTS_SAMPLE_RATE * ms // 1000, dtype="<i2")


def _frames(pcm: np.ndarray) -> Iterator[np.ndarray]:
    """Cut PCM into playback-sized frames so audio starts sooner."""
    for i in range(0, len(pcm), MAX_FRAME_SAMPLES):
        yield pcm[i : i + MAX_FRAME_SAMPLES]


# --- App --------------------------------------------------------------------

state: dict[str, Any] = {"stt": None, "tts": None, "ready": False, "error": ""}


def _load_models() -> None:
    try:
        stt, tts = STT(), TTS()
        t0 = time.monotonic()
        stt.warm()
        tts.warm()
        log.info("warmup done in %.2fs", time.monotonic() - t0)
        state["stt"], state["tts"], state["ready"] = stt, tts, True
    except Exception as e:  # noqa: BLE001 — report, don't crash the container
        state["error"] = f"{type(e).__name__}: {e}"
        log.exception("model load failed")


@asynccontextmanager
async def lifespan(_: FastAPI) -> AsyncIterator[None]:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    # Loading and warming the models takes tens of seconds (minutes on the first
    # run, which downloads the weights). It happens on a worker thread that is
    # *not* awaited here, so the port opens immediately and /healthz can report
    # progress instead of the container looking hung.
    task = asyncio.create_task(asyncio.to_thread(_load_models))
    try:
        yield
    finally:
        task.cancel()


app = FastAPI(title="telemetry-handler voice server", lifespan=lifespan)


@app.get("/healthz")
async def healthz() -> JSONResponse:
    body = {
        "ready": state["ready"],
        "error": state["error"],
        "stt": {
            "model": STT_MODEL,
            "device": STT_DEVICE,
            "compute_type": STT_COMPUTE,
            "language": STT_LANGUAGE,
            "sample_rate": STT_SAMPLE_RATE,
            "partials": STT_PARTIALS,
        },
        "tts": {
            "model": TTS_REPO,
            "device": TTS_DEVICE,
            "voice": TTS_VOICE,
            "sample_rate": TTS_SAMPLE_RATE,
            "format": "s16le",
            "channels": 1,
        },
    }
    return JSONResponse(body, status_code=200 if state["ready"] else 503)


async def _authorize(ws: WebSocket) -> bool:
    """Accept the socket, refusing it if the shared secret does not match."""
    if TOKEN and ws.query_params.get("token") != TOKEN:
        await ws.close(code=4401, reason="unauthorized")
        return False
    await ws.accept()
    if not state["ready"]:
        await _send(ws, {"type": "error", "message": state["error"] or "models still loading"})
        await ws.close(code=1013, reason="not ready")
        return False
    return True


async def _send(ws: WebSocket, payload: dict[str, Any]) -> None:
    await ws.send_text(json.dumps(payload))


# --- STT socket -------------------------------------------------------------


@app.websocket("/v1/stt")
async def stt_socket(ws: WebSocket) -> None:
    if not await _authorize(ws):
        return
    stt: STT = state["stt"]
    buf = bytearray()
    language = STT_LANGUAGE
    partial_task: asyncio.Task | None = None
    last_partial = 0.0

    async def emit_partial(snapshot: bytes) -> None:
        try:
            text = await stt.transcribe(pcm_to_float(snapshot), language)
            if text:
                await _send(ws, {"type": "partial", "text": text})
        except Exception as e:  # noqa: BLE001 — a partial is best-effort
            log.debug("stt: partial failed: %s", e)

    try:
        while True:
            msg = await ws.receive()
            if msg["type"] == "websocket.disconnect":
                return

            chunk = msg.get("bytes")
            if chunk is not None:
                buf.extend(chunk)
                if (
                    STT_PARTIALS
                    and (partial_task is None or partial_task.done())
                    and (time.monotonic() - last_partial) * 1000 >= STT_PARTIAL_MS
                ):
                    last_partial = time.monotonic()
                    partial_task = asyncio.create_task(emit_partial(bytes(buf)))
                continue

            text = msg.get("text")
            if text is None:
                continue
            req = json.loads(text)
            kind = req.get("type")
            if kind == "start":
                buf.clear()
                language = (req.get("language") or STT_LANGUAGE).strip()
                last_partial = time.monotonic()
            elif kind == "cancel":
                buf.clear()
            elif kind == "ping":
                await _send(ws, {"type": "pong"})
            elif kind == "eos":
                if partial_task and not partial_task.done():
                    partial_task.cancel()
                audio = pcm_to_float(bytes(buf))
                buf.clear()
                if len(audio) < STT_SAMPLE_RATE * STT_MIN_MS // 1000:
                    await _send(ws, {"type": "final", "text": ""})
                    continue
                t0 = time.monotonic()
                try:
                    result = await stt.transcribe(audio, language)
                except Exception as e:  # noqa: BLE001 — keep the socket usable
                    log.exception("stt: decode failed")
                    await _send(ws, {"type": "error", "message": f"{type(e).__name__}: {e}"})
                    continue
                log.info(
                    "stt: %.2fs audio -> %.0fms -> %r",
                    len(audio) / STT_SAMPLE_RATE,
                    (time.monotonic() - t0) * 1000,
                    result,
                )
                await _send(ws, {"type": "final", "text": result})
    except WebSocketDisconnect:
        return
    finally:
        if partial_task and not partial_task.done():
            partial_task.cancel()


# --- TTS socket -------------------------------------------------------------


@app.websocket("/v1/tts")
async def tts_socket(ws: WebSocket) -> None:
    if not await _authorize(ws):
        return
    tts: TTS = state["tts"]
    job: asyncio.Task | None = None

    async def cancel_job() -> None:
        nonlocal job
        if job and not job.done():
            job.cancel()
            try:
                await job
            except asyncio.CancelledError:
                pass
        job = None

    try:
        while True:
            req = json.loads(await ws.receive_text())
            kind = req.get("type")
            if kind == "speak":
                # A new message supersedes whatever is still playing: in the car,
                # the latest call is the one that matters.
                await cancel_job()
                job = asyncio.create_task(_speak(ws, tts, req))
            elif kind == "cancel":
                await cancel_job()
            elif kind == "ping":
                await _send(ws, {"type": "pong"})
    except (WebSocketDisconnect, RuntimeError):
        return
    finally:
        await cancel_job()


async def _speak(ws: WebSocket, tts: TTS, req: dict[str, Any]) -> None:
    text = (req.get("text") or "").strip()
    if not text:
        await _send(ws, {"type": "end"})
        return
    voice = req.get("voice") or TTS_VOICE
    speed = float(req.get("speed") or TTS_SPEED)

    loop = asyncio.get_running_loop()
    queue: asyncio.Queue = asyncio.Queue()
    stop = threading.Event()
    t0 = time.monotonic()

    def produce() -> None:
        # Synthesis is blocking CUDA work, so it runs on its own thread and hands
        # finished frames back to the event loop as they appear.
        try:
            for frame in tts.stream(text, voice, speed):
                if stop.is_set():
                    return
                loop.call_soon_threadsafe(queue.put_nowait, frame)
            loop.call_soon_threadsafe(queue.put_nowait, None)
        except Exception as e:  # noqa: BLE001 — surfaced to the client below
            loop.call_soon_threadsafe(queue.put_nowait, e)

    threading.Thread(target=produce, daemon=True).start()
    try:
        await _send(
            ws,
            {
                "type": "begin",
                "sample_rate": TTS_SAMPLE_RATE,
                "channels": 1,
                "format": "s16le",
            },
        )
        first = True
        while True:
            item = await queue.get()
            if item is None:
                break
            if isinstance(item, Exception):
                log.exception("tts: synthesis failed", exc_info=item)
                await _send(ws, {"type": "error", "message": f"{type(item).__name__}: {item}"})
                return
            if first:
                log.info("tts: first chunk in %.0fms for %r", (time.monotonic() - t0) * 1000, text)
                first = False
            await ws.send_bytes(item.tobytes())
        await _send(ws, {"type": "end"})
    finally:
        stop.set()
