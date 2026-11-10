# Voice notes (Whisper)

Record a voice note in Life OS (the microphone next to the capture box, or the phone and watch apps), and Life OS turns it into text on your own server with [whisper.cpp](https://github.com/ggml-org/whisper.cpp). The text lands in Inbox like a typed note; with [AI processing](ai-processing.md) on, it is filed from there. The recording never leaves your server and is deleted after 7 days (`LIFE_OS_VOICE_RETENTION_HOURS`).

## Choose a model

Whisper understands many languages, Russian, German, and English among them. A bigger model is more accurate, slower, and needs more memory. On its first start, Whisper downloads the model you chose from Hugging Face; voice notes wait until it is there.

| Model | Download | Memory while transcribing | |
| --- | --- | --- | --- |
| `tiny` | 75 MB | about 0.3 GB | fast, rough |
| `base` | 142 MB | about 0.4 GB | |
| `small` | 466 MB | about 0.9 GB | **the default**, good for most languages |
| `medium` | 1.5 GB | about 2.1 GB | |
| `large-v3-turbo` | 1.6 GB | more than `medium` | the most accurate here |

The memory figures are whisper.cpp's own. To change the model:

```sh
./setup.sh whisper
./setup.sh up
```

It sets the model and Whisper's memory cap (`LIFE_OS_WHISPER_MEMORY`: 2 GB, or 3 GB for `medium` and `large-v3-turbo`). The old model file stays in the `lifeos-whisper-models` volume.

## CPU and memory

Whisper uses at most 2 CPUs (`LIFE_OS_WHISPER_CPUS`, `LIFE_OS_WHISPER_THREADS`) and its memory cap, and transcribes one note at a time; more CPUs make it faster. On a small server, pick a smaller model rather than raising the memory cap.

More: [Voice and STT](../services/voice-stt.md).
