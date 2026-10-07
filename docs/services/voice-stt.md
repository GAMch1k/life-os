# Voice and STT

Voice ingestion is an asynchronous workflow: `POST /v1/audio-jobs` stores an audio object and returns a `202` job immediately; clients poll `GET /v1/audio-jobs/{jobId}`. A worker claims queued/retryable jobs, invokes self-hosted `whisper.cpp`, persists the transcript, and creates exactly one Inbox item before marking the job `succeeded`.

Jobs move through `queued` → `processing` → `succeeded`. Transient worker errors become `retryable` with a scheduled `availableAt`; exhausted retries become `failed`. Uploads and completed-job audio are retained only until `expiresAt`, after which cleanup marks the job `expired` and removes the object. A unique job-to-Inbox linkage prevents a retry from producing duplicate captures.

The API exposes source, audit timestamps, transcript, error summary, attempts, and the resulting Inbox ID. Browser capture may poll; it must never wait for transcription synchronously. P6-03 implements upload storage, claiming, retries, and worker execution; P6-02 supplies the private whisper worker. Android and Wear OS capture clients remain separate repositories.

## Job processing and recovery

Audio is stored in the private `voice-audio` volume and a job is committed as `queued` before the upload endpoint returns `202`. The worker claims rows with `FOR UPDATE SKIP LOCKED`, so process restarts leave a recoverable queued/retryable record instead of losing an upload. It calls the private Whisper worker and captures the transcript using a deterministic `voice:<job-id>` Inbox dedupe key. Consequently, a retry after an incomplete handoff returns the original Inbox item instead of creating another capture.

Transient errors record `retryable` plus an error summary and delayed availability; the configured maximum changes that state to `failed`. Expired audio is removed from the private volume and its job moves to `expired`, preserving audit information but removing the transcript/audio content.

## Whisper worker

`docker compose up whisper` builds and starts a private CPU-only `whisper.cpp` HTTP worker. It downloads the configured model once into the named `whisper-models` volume, accepts a raw audio body at `POST /transcribe`, and returns JSON transcript text. `GET /ready` confirms the selected model is present. The default multilingual `base` model supports Russian, German, and English; set `LIFE_OS_WHISPER_MODEL_PATH` and `LIFE_OS_WHISPER_MODEL_URL` together to use another model.

`LIFE_OS_WHISPER_THREADS`, `LIFE_OS_WHISPER_MAX_CONCURRENCY`, `LIFE_OS_WHISPER_CPUS`, and `LIFE_OS_WHISPER_MEMORY` constrain resource use. A busy worker returns `429`; invalid audio, model, and process failures return a useful error for the job processor. The worker is intentionally not gateway-routed or host-published.
