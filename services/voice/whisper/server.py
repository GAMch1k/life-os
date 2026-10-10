import json
import os
import shutil
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

MODEL = Path(os.environ["WHISPER_MODEL_PATH"])
MODEL_URL = os.environ.get("WHISPER_MODEL_URL", "")
THREADS = os.environ.get("WHISPER_THREADS", "2")
MAX_BYTES = int(os.environ.get("WHISPER_MAX_AUDIO_BYTES", "26214400"))
SLOTS = threading.BoundedSemaphore(int(os.environ.get("WHISPER_MAX_CONCURRENCY", "1")))
CONVERT_TIMEOUT_SECONDS = 300
# Containers Voice accepts (WAV, Ogg, WebM/Matroska, MP4/M4A/3GP). Anything
# else is refused by ffmpeg, and only local files may be opened, so a crafted
# upload cannot make ffmpeg read playlists, URLs or other files.
INPUT_FORMATS = "wav,ogg,matroska,webm,mov,mp4,m4a,3gp,3g2"

def convert_command(source, target):
    """ffmpeg arguments that turn the first audio stream of source into the
    16 kHz mono 16-bit PCM WAV whisper.cpp expects."""
    return ["ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
            "-protocol_whitelist", "file", "-format_whitelist", INPUT_FORMATS,
            "-i", str(source), "-map", "0:a:0", "-vn", "-sn", "-dn",
            "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-f", "wav", "-y", str(target)]

def ensure_model():
    if MODEL.exists():
        return
    if not MODEL_URL:
        raise RuntimeError("WHISPER_MODEL_PATH does not exist and WHISPER_MODEL_URL is empty")
    MODEL.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["wget", "-q", "-O", str(MODEL), MODEL_URL], check=True)

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/ready":
            self.send_error(404); return
        self.respond(200, {"status": "ready", "model": str(MODEL)})
    def do_POST(self):
        if self.path != "/transcribe":
            self.send_error(404); return
        size = int(self.headers.get("Content-Length", "0"))
        if size <= 0 or size > MAX_BYTES:
            self.respond(413, {"error": "audio is empty or exceeds WHISPER_MAX_AUDIO_BYTES"}); return
        data = self.rfile.read(size)
        if not SLOTS.acquire(blocking=False):
            self.respond(429, {"error": "worker is busy"}); return
        try:
            # The directory, the upload and the converted WAV are removed on
            # every path out of this block.
            with tempfile.TemporaryDirectory() as directory:
                audio = Path(directory) / "audio.bin"
                wav = Path(directory) / "audio.wav"
                output = Path(directory) / "transcript"
                audio.write_bytes(data)
                try:
                    converted = subprocess.run(convert_command(audio, wav), capture_output=True, text=True, timeout=CONVERT_TIMEOUT_SECONDS)
                except subprocess.TimeoutExpired:
                    self.respond(504, {"error": "audio conversion timed out"}); return
                if converted.returncode != 0 or not wav.exists():
                    self.respond(422, {"error": "audio conversion failed", "detail": converted.stderr[-1000:]}); return
                audio.unlink()
                result = subprocess.run(["whisper-cli", "-m", str(MODEL), "-l", "auto", "-f", str(wav), "-otxt", "-of", str(output), "-t", THREADS], capture_output=True, text=True, timeout=1800)
                text_file = output.with_suffix(".txt")
                if result.returncode != 0 or not text_file.exists():
                    self.respond(422, {"error": "whisper transcription failed", "detail": result.stderr[-1000:]}); return
                self.respond(200, {"transcript": text_file.read_text().strip()})
        except subprocess.TimeoutExpired:
            self.respond(504, {"error": "whisper transcription timed out"})
        finally:
            SLOTS.release()
    def respond(self, code, body):
        raw = json.dumps(body).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(raw))); self.end_headers(); self.wfile.write(raw)
    def log_message(self, fmt, *args): pass

if __name__ == "__main__":
    if shutil.which("ffmpeg") is None:
        raise RuntimeError("ffmpeg is required to normalize audio before transcription")
    ensure_model()
    ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
