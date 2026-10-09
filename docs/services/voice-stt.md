# Voice and STT

Voice ingestion is an asynchronous workflow: `POST /v1/audio-jobs` stores an audio object and returns a `202` job immediately; clients poll `GET /v1/audio-jobs/{jobId}`. A worker claims queued/retryable jobs, invokes self-hosted `whisper.cpp`, persists the transcript, and creates exactly one Inbox item before marking the job `succeeded`.

Audio ingress accepts only sniffed WAV, Ogg, or WebM payloads; the caller MIME type is not trusted. `LIFE_OS_VOICE_MAX_UPLOAD_BYTES` is 25 MiB (26,214,400 bytes) and is enforced before copying a file into the private audio volume. The Docker gateway allows 26 MiB to cover multipart framing, and Whisper uses the same 25 MiB payload maximum. Oversized uploads return 413; invalid or misleading content returns 400 and leaves no persistent audio object.

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

`docker compose up whisper` builds and starts a private CPU-only `whisper.cpp` HTTP worker. It downloads the configured model once into the named `whisper-models` volume, accepts a raw audio body at `POST /transcribe`, and returns JSON transcript text. `GET /ready` confirms the selected model is present. The default multilingual `base` model supports Russian, German, and English; set `LIFE_OS_WHISPER_MODEL_PATH` and `LIFE_OS_WHISPER_MODEL_URL` together to use another model.

`LIFE_OS_WHISPER_THREADS`, `LIFE_OS_WHISPER_MAX_CONCURRENCY`, `LIFE_OS_WHISPER_CPUS`, and `LIFE_OS_WHISPER_MEMORY` constrain resource use. A busy worker returns `429`; invalid audio, model, and process failures return a useful error for the job processor. The worker is intentionally not gateway-routed or host-published.

A Voice job is the root of its workflow, and its id is the activity correlation id. Upload records `voice.job.accepted`. The worker records `voice.job.transcribed`, or `voice.job.retry_scheduled`/`voice.job.failed`. When the worker creates the Inbox item, it sends the job id as `X-Correlation-ID` and the transcription event as `X-Causation-ID`. The later processing, MCP action, and resulting Task therefore all appear in the same trace. See [Activity](activity.md).
