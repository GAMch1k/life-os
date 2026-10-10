# Voice and STT

Voice ingestion is an asynchronous workflow: `POST /v1/audio-jobs` stores an audio object and returns a `202` job immediately; clients poll `GET /v1/audio-jobs/{jobId}`. A worker claims queued/retryable jobs, invokes self-hosted `whisper.cpp`, persists the transcript, and creates exactly one Inbox item before marking the job `succeeded`.

Audio ingress accepts the containers browsers record, recognized by sniffing the leading bytes; the declared MIME type and file name are not trusted:

| Container                                           | Recorded by                                       | Recorded content type |
| --------------------------------------------------- | ------------------------------------------------- | --------------------- |
| WebM/Matroska (Opus)                                | Chrome, Edge, other Chromium browsers             | `audio/webm`          |
| Ogg with an audio codec (Opus, Vorbis, FLAC, Speex) | Firefox                                           | `audio/ogg`           |
| ISO BMFF with an audio brand (MP4, M4A, 3GP)        | Safari and iOS (AAC); also Chromium's `audio/mp4` | `audio/mp4`           |
| WAV                                                 | tools and fixtures                                | `audio/wave`          |

Anything else is refused with 400 `unsupported_audio`, including Ogg/Theora video, HEIC/AVIF images (which share the MP4 `ftyp` box) and QuickTime files. `LIFE_OS_VOICE_MAX_UPLOAD_BYTES` is 25 MiB (26,214,400 bytes) and is enforced before copying a file into the private audio volume; oversized audio returns 413 `audio_too_large`. Invalid content leaves no persistent audio object. The Voice API's HTTP body limit is that maximum plus 1 MiB for multipart framing; Fiber's own 4 MiB default would otherwise refuse longer recordings. The Docker gateway also allows 26 MiB, and Whisper uses the same 25 MiB payload maximum.

The Dashboard checks the 25 MiB limit before uploading, because an over-limit body is refused by the gateway before it is read: through the Cloudflare tunnel that 413 can arrive as a 502 or a dropped connection. For the voice location the gateway keeps reading and discarding an over-limit body for up to 2 minutes (`lingering_time 120s`) so that its 413 can reach a slower client intact. The Dashboard shows a size message for a 413, and for a 502 or dropped connection on an over-limit recording. The client-side limit is a constant (`VOICE_MAX_UPLOAD_BYTES` in `apps/dashboard/src/lib/voice-job-state.ts`); change it together with `LIFE_OS_VOICE_MAX_UPLOAD_BYTES`.

The Voice UI asks MediaRecorder for `audio/ogg;codecs=opus`, `audio/webm;codecs=opus`, then `audio/mp4`, and falls back to the browser default. Firefox therefore records Ogg/Opus, Chromium browsers (which cannot record Ogg) WebM/Opus, and Safari MP4. The upload's file name carries the matching extension.

Jobs move through `queued` → `processing` → `succeeded`. Transient worker errors become `retryable` with a scheduled `availableAt`; exhausted retries become the observable terminal `failed` state. Uploads and completed-job audio are retained only until `expiresAt`, after which cleanup marks the job `expired` and removes the object. A unique job-to-Inbox linkage prevents a retry from producing duplicate captures.

The API exposes source, audit timestamps, transcript, error summary, attempts, and the resulting Inbox ID. Browser capture may poll; it must never wait for transcription synchronously. P6-03 implements upload storage, claiming, retries, and worker execution; P6-02 supplies the private whisper worker. Android and Wear OS capture clients remain separate repositories.

## Job processing and recovery

Audio is stored in the private `voice-audio` volume and a job is committed as `queued` before the upload endpoint returns `202`. The worker claims rows with `FOR UPDATE SKIP LOCKED` and records its worker identity, lease expiry, and heartbeat. It renews the lease while Whisper and Inbox handoff run. On every worker start/poll, expired `processing` leases are reclaimed without a manual database edit: jobs below the configured attempt limit return to `retryable` after backoff, while exhausted jobs become `failed`. A stale worker cannot complete or retry a job after it loses its lease.

`VOICE_WORKER_LEASE_SECONDS` defaults to `120`; `VOICE_WORKER_RETRY_BACKOFF_SECONDS` defaults to `60`; and `VOICE_WORKER_MAX_ATTEMPTS` defaults to `3`. Ordinary retry delay grows linearly by attempt count, while an abandoned lease uses the configured base delay before the next worker can claim it. Cleanup never removes audio for an actively leased `processing` job, even if its retention deadline has elapsed; it can expire it only after the lease is no longer active.

The worker calls the private Whisper service and captures the transcript using a deterministic `voice:<job-id>` Inbox dedupe key. Consequently, a reclaim/retry after an incomplete handoff returns the original Inbox item instead of creating another capture.

Transient errors record `retryable` plus an error summary and delayed availability; the configured maximum changes that state to `failed`. Expired audio is removed from the private volume and its job moves to `expired`, preserving audit information but removing the transcript/audio content.

## Backlog and health

`GET /v1/backlog` summarizes unfinished transcription work for status surfaces:

- queued, transcribing, and retryable job counts;
- the oldest unfinished job and the earliest scheduled retry (`nextRetryAt`);
- the latest failure text;
- `workerStalled`: true when a processing lease has expired, or when due jobs have waited over two minutes while nothing is processing;
- `whisperReady`: whether Whisper answered its `/ready` probe within two seconds. The Voice API uses `WHISPER_SERVICE_URL` (default `http://whisper:8080`).

## Whisper worker

`docker compose up whisper` builds and starts a private CPU-only `whisper.cpp` HTTP worker. It downloads the configured model once into the named `whisper-models` volume, accepts a raw audio body at `POST /transcribe`, and returns JSON transcript text. `GET /ready` confirms the selected model is present.

`whisper-cli` reads only WAV, so the worker first converts every upload with ffmpeg (included in the image) to 16 kHz mono 16-bit PCM WAV, taking the first audio stream. ffmpeg may open only local files in the accepted container formats, so a crafted upload cannot make it fetch URLs or read playlists. Both files live in a per-request temporary directory that is removed afterwards. A file ffmpeg cannot decode returns 422 `audio conversion failed` with ffmpeg's error, and a conversion running over 5 minutes returns 504; the Voice worker records either as the job's error. The default multilingual `base` model supports Russian, German, and English; set `LIFE_OS_WHISPER_MODEL_PATH` and `LIFE_OS_WHISPER_MODEL_URL` together to use another model.

`LIFE_OS_WHISPER_THREADS`, `LIFE_OS_WHISPER_MAX_CONCURRENCY`, `LIFE_OS_WHISPER_CPUS`, and `LIFE_OS_WHISPER_MEMORY` constrain resource use. A busy worker returns `429`; invalid audio, model, and process failures return a useful error for the job processor. The worker is intentionally not gateway-routed or host-published.

A Voice job is the root of its workflow, and its id is the activity correlation id. Upload records `voice.job.accepted`. The worker records `voice.job.transcribed`, or `voice.job.retry_scheduled`/`voice.job.failed`. When the worker creates the Inbox item, it sends the job id as `X-Correlation-ID` and the transcription event as `X-Causation-ID`. The later processing, MCP action, and resulting Task therefore all appear in the same trace. See [Activity](activity.md).

## Verification

The browser spec `apps/dashboard/e2e/voice-capture.spec.ts` records with the browsers' real MediaRecorder; only the microphone is simulated. Chromium's fake audio device plays a spoken WAV (WebM/Opus recording). In Firefox, whose fake device only produces a tone, a captured `<audio>` element playing the same WAV is the microphone (Ogg/Opus recording). Both recordings must be transcribed into an Inbox item. API checks in the same spec upload Ogg/Opus and Safari-style MP4/AAC, a 5 MiB WAV, non-audio and over-limit audio. Playwright runs the voice spec in Firefox too when Playwright's Firefox is installed (`npx playwright install firefox` in `apps/dashboard`); every other spec runs in Chromium only.

The fixtures in `apps/dashboard/e2e/fixtures/` hold "I already bought the chicken." spoken by espeak-ng and encoded with ffmpeg. The WAV peaks at -13 dBFS: louder input is clipped by Chromium's capture processing and misrecognized. To regenerate them:

```sh
docker run --rm -v "$PWD:/out" alpine:3.22 sh -c \
  "apk add --no-cache espeak-ng >/dev/null && espeak-ng -v en-us -s 125 -w /out/espeak.wav 'I already bought the chicken.'"
B="-map_metadata -1 -fflags +bitexact -flags:a +bitexact"
ffmpeg -i espeak.wav $B -af "adelay=500:all=1,apad=pad_dur=0.7,volume=-12dB" -ac 1 -ar 16000 -c:a pcm_s16le chicken.wav
ffmpeg -i chicken.wav $B -c:a libopus -b:a 32k -f webm chicken.webm
ffmpeg -i chicken.wav $B -c:a libopus -b:a 32k -f ogg chicken.ogg
ffmpeg -i chicken.wav $B -ar 44100 -c:a aac -b:a 64k -movflags frag_keyframe+empty_moov+default_base_moof -f mp4 chicken.m4a
```

The fragmented MP4 has the layout Safari's MediaRecorder writes. `chicken.webm` is not used by the spec (the Chromium recording covers WebM) but is handy for a manual upload check.
