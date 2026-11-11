# Voice and STT

Voice ingestion is an asynchronous workflow: `POST /v1/audio-jobs` stores an audio object and returns a `202` job immediately; clients poll `GET /v1/audio-jobs/{jobId}`. A worker claims queued/retryable jobs, invokes self-hosted `whisper.cpp`, persists the transcript, and creates exactly one Inbox item before marking the job `succeeded`. A recording without speech (an empty transcript, or only Whisper's non-speech annotations such as `[BLANK_AUDIO]`, `[ Silence ]`, or `(music)`) is an outcome, not a Whisper failure: the job is `failed` at once, without retries and without an Inbox item, with the error summary "No speech was heard in the recording. Record it again." and a `voice.job.failed` Activity event ("No speech was heard in the recording", `data.reason: no_speech`), so such recordings stay trackable. A transcript with words keeps its annotations. A malformed Whisper answer or an unreachable Whisper stays a retryable failure.

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

## Retried uploads

An upload may carry an `Idempotency-Key` header, as Tasks creates do. The Android and Wear OS apps queue recordings and retry with backoff, sending the capture id as the key on every attempt (and in `sourceMetadata`). If Voice accepted a recording but the reply was lost, the retry gets the same job back instead of a second job, so the transcript and the Inbox item appear once.

- The key is 1 to 200 visible ASCII characters (no spaces); anything else is 400 `invalid_request`. An empty header is no key.
- A repeat with the same key, the same audio content (by SHA-256), the same `source`, and the same `sourceMetadata` (compared as JSON, so key order and spacing do not matter) stores nothing and returns the job the first upload created, in its current state: `queued`, `processing`, `succeeded` with its transcript and `inboxItemId`, `failed`, or `expired`. It is answered 202, as the first upload was, and records no Activity event.
- The same key with other audio, another `source`, or other metadata is 409 `idempotency_conflict`, and nothing is stored.
- Without the header every upload creates a job, as before.

The key and a hash of what the upload asked for are kept on the job (`idempotency_key`, `request_hash`; migration 000003, with a unique partial index on the key), for as long as the job exists: an expired job keeps them, so a very late retry still gets the expired job rather than a new one. The multipart body is not hashed as sent, because its boundary changes with every retry. A retry is recognized before its audio is stored. When two uploads with one key arrive together, both may store their audio, but the database lets only one create the job; the other removes its copy and answers like a retry (or 409 if it was a different upload).

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

`whisper` is a private CPU-only `whisper.cpp` HTTP worker (`docker compose build whisper`, then `docker compose up -d --no-build --no-deps whisper`). It downloads the configured model once into the named `whisper-models` volume, accepts a raw audio body at `POST /transcribe`, and returns JSON transcript text. `GET /ready` confirms the selected model is present.

`whisper-cli` is built for any x86-64 processor with AVX2, FMA, and F16C (`GGML_NATIVE=OFF`), never for the build machine's own: v0.1.0-preview.5's image, built on a release runner with AVX-512, stopped while loading the model on every processor without it, so its voice notes failed. The build fails when the binary has AVX-512 instructions.

`whisper-cli` reads only WAV, so the worker first converts every upload with ffmpeg (included in the image) to 16 kHz mono 16-bit PCM WAV, taking the first audio stream. ffmpeg may open only local files in the accepted container formats, so a crafted upload cannot make it fetch URLs or read playlists. Both files live in a per-request temporary directory that is removed afterwards. A file ffmpeg cannot decode returns 422 `audio conversion failed` with ffmpeg's error, and a conversion running over 5 minutes returns 504; the Voice worker records either as the job's error. The model is the multilingual **`small`** model (`ggml-small.bin`, about 466 MB), the default in `compose.yaml` and `.env.example` and the deployment's since 2026-10-12. It supports Russian, German, and English, and is noticeably better than the smaller `base` for Russian and German at about three times the time; the owner prefers accuracy over speed, so `large-v3-turbo` is the next step if `small` is not good enough. Set `LIFE_OS_WHISPER_MODEL_PATH` and `LIFE_OS_WHISPER_MODEL_URL` together to use another model. A new model downloads into the `whisper-models` volume when the worker starts; the old file stays until removed.

The spoken language is `LIFE_OS_WHISPER_LANGUAGE` (`WHISPER_LANGUAGE` in the container), passed to `whisper-cli` as `-l`:

- `auto`, the default, makes whisper.cpp detect the language of each recording. Short notes on small models are sometimes detected wrong.
- One whisper.cpp language code, such as `de`, `ru`, or `en`, transcribes every recording as that language: more accurate for an owner who speaks one language, and a little faster, since detection is skipped.
- The codes are whisper.cpp's own (`g_lang` in its `src/whisper.cpp`), listed in `services/voice/whisper/languages.txt`. The image build fails when that list differs from the source of the whisper.cpp version it builds, so a whisper.cpp update that adds or drops a language has to update the list, and `setup.sh`'s copy, which `tools/test-setup` compares with it.
- The value is trimmed and lower-cased. Anything else stops the worker at its start, before any model download, with exit code 78 and a log line naming the value: it never falls back to `auto` silently.
- `GET /ready` reports it next to the model: `{"status": "ready", "model": "/models/ggml-small.bin", "language": "auto"}`.

The deployment sets no `LIFE_OS_WHISPER_LANGUAGE`, so it stays on `auto`.

`LIFE_OS_WHISPER_THREADS`, `LIFE_OS_WHISPER_MAX_CONCURRENCY`, `LIFE_OS_WHISPER_CPUS`, and `LIFE_OS_WHISPER_MEMORY` constrain resource use. A busy worker returns `429`; invalid audio, model, and process failures return a useful error for the job processor. The worker is intentionally not gateway-routed or host-published.

A Voice job is the root of its workflow, and its id is the activity correlation id. Upload records `voice.job.accepted`. The worker records `voice.job.transcribed`, or `voice.job.retry_scheduled`/`voice.job.failed`. When the worker creates the Inbox item, it sends the job id as `X-Correlation-ID` and the transcription event as `X-Causation-ID`. The later processing, MCP action, and resulting Task therefore all appear in the same trace. See [Activity](activity.md).

## Verification

The Go integration tests run against the Postgres named by `VOICE_TEST_DATABASE_URL` (a throwaway database: each test creates and drops its own schema and applies the migrations, see `internal/testdb`) and skip when it is unset. Retried uploads are tested over HTTP in `internal/api/idempotency_integration_test.go` (a retry returns the same job and stores no second copy, a reused key with other audio, metadata, or source is 409, uploads without a key or with other keys create jobs, concurrent uploads with one key create one job, and an upload that loses the race removes its audio), in `cmd/worker/idempotency_integration_test.go` (retries before and after transcription give one job and one Inbox item), and migration 000003 down and up in `internal/repository/idempotency_integration_test.go`.


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
