# Voice and STT

Voice ingestion is an asynchronous workflow: `POST /v1/audio-jobs` stores an audio object and returns a `202` job immediately; clients poll `GET /v1/audio-jobs/{jobId}`. A worker claims queued/retryable jobs, invokes self-hosted `whisper.cpp`, persists the transcript, and creates exactly one Inbox item before marking the job `succeeded`.

Jobs move through `queued` → `processing` → `succeeded`. Transient worker errors become `retryable` with a scheduled `availableAt`; exhausted retries become `failed`. Uploads and completed-job audio are retained only until `expiresAt`, after which cleanup marks the job `expired` and removes the object. A unique job-to-Inbox linkage prevents a retry from producing duplicate captures.

The API exposes source, audit timestamps, transcript, error summary, attempts, and the resulting Inbox ID. Browser capture may poll; it must never wait for transcription synchronously. P6-03 implements upload storage, claiming, retries, and worker execution; P6-02 supplies the private whisper worker. Android and Wear OS capture clients remain separate repositories.
