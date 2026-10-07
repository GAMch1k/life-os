import json
import os
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
            with tempfile.TemporaryDirectory() as directory:
                audio = Path(directory) / "audio.bin"
                output = Path(directory) / "transcript"
                audio.write_bytes(data)
                result = subprocess.run(["whisper-cli", "-m", str(MODEL), "-l", "auto", "-f", str(audio), "-otxt", "-of", str(output), "-t", THREADS], capture_output=True, text=True, timeout=1800)
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
    ensure_model()
    ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
