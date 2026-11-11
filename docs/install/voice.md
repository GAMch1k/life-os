# Voice notes (Whisper)

Voice notes are the `voice` [module](modules.md), off on a new install: `./setup.sh whisper` (or `./setup.sh modules on voice`) turns them on, then `./setup.sh up`. `./setup.sh modules off voice` turns them off again; the recordings and the model stay in their volumes.

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

## Language

By default (`auto`), Whisper guesses the language of each voice note. On a short note, a small model sometimes guesses wrong, and the text comes out in the wrong language or garbled. If you speak one language only, set it: the transcripts are more accurate, and a bit faster, since Whisper skips the guess. A note in another language then comes out wrong, so keep `auto` if you switch languages.

`./setup.sh whisper` asks for it after the model; Enter keeps the current one. Answer `auto`, or one of whisper.cpp's language codes, such as `en` (English), `de` (German), `ru` (Russian), `uk` (Ukrainian), `fr`, `es`, `it`, or `pl`. It goes into `.env` as `LIFE_OS_WHISPER_LANGUAGE`; apply it with `./setup.sh up`. `./setup.sh status` shows it next to the model.

Every code whisper.cpp knows: `af` `am` `ar` `as` `az` `ba` `be` `bg` `bn` `bo` `br` `bs` `ca` `cs` `cy` `da` `de` `el` `en` `es` `et` `eu` `fa` `fi` `fo` `fr` `gl` `gu` `ha` `haw` `he` `hi` `hr` `ht` `hu` `hy` `id` `is` `it` `ja` `jw` `ka` `kk` `km` `kn` `ko` `la` `lb` `ln` `lo` `lt` `lv` `mg` `mi` `mk` `ml` `mn` `mr` `ms` `mt` `my` `ne` `nl` `nn` `no` `oc` `pa` `pl` `ps` `pt` `ro` `ru` `sa` `sd` `si` `sk` `sl` `sn` `so` `sq` `sr` `su` `sv` `sw` `ta` `te` `tg` `th` `tk` `tl` `tr` `tt` `uk` `ur` `uz` `vi` `yi` `yo` `yue` `zh`.

Whisper refuses a code it doesn't know: it stops at its start, and `docker compose logs whisper` says why. Voice notes wait until it is fixed.

## CPU and memory

Whisper uses at most 2 CPUs (`LIFE_OS_WHISPER_CPUS`, `LIFE_OS_WHISPER_THREADS`) and its memory cap, and transcribes one note at a time; more CPUs make it faster. On a small server, pick a smaller model rather than raising the memory cap.

More: [Voice and STT](../services/voice-stt.md).
