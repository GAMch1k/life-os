import type { components } from "@/lib/api/generated/voice";

export type VoiceJob = components["schemas"]["AudioJob"];

export function isVoiceJobTerminal(status: VoiceJob["status"]) {
  return status === "succeeded" || status === "failed" || status === "expired";
}

export function voiceJobMessage(job: VoiceJob) {
  switch (job.status) {
    case "queued":
      return "Voice capture accepted and queued for transcription.";
    case "processing":
      return "Transcribing your recording in the background…";
    case "retryable":
      return "Transcription will retry automatically. You can keep this page open for updates.";
    case "succeeded":
      return job.inboxItemId
        ? "Transcribed and added to Inbox."
        : "Transcription finished.";
    case "failed":
      return job.errorSummary || "Transcription failed. You can record again.";
    case "expired":
      return "This recording expired before it could be kept. Record it again.";
  }
}

/**
 * The Voice upload limit: the default LIFE_OS_VOICE_MAX_UPLOAD_BYTES (25 MiB).
 * The browser checks it before uploading, because an over-limit body is
 * refused by the gateway before it is read, which a Cloudflare tunnel turns
 * into a bare 502.
 */
export const VOICE_MAX_UPLOAD_BYTES = 25 * 1024 * 1024;

/**
 * MediaRecorder formats in order of preference; all of them are accepted and
 * normalized by Voice. Firefox records Ogg/Opus, Chromium browsers (no Ogg
 * recording) WebM/Opus, and Safari MP4. When none is supported the browser's
 * default is used.
 */
export const VOICE_RECORDING_TYPES = [
  "audio/ogg;codecs=opus",
  "audio/webm;codecs=opus",
  "audio/mp4",
] as const;

export function pickRecordingType(isTypeSupported: (type: string) => boolean) {
  return VOICE_RECORDING_TYPES.find((type) => isTypeSupported(type));
}

const RECORDING_EXTENSIONS: Record<string, string> = {
  "audio/ogg": "ogg",
  "audio/webm": "webm",
  "video/webm": "webm",
  "audio/x-matroska": "mka",
  "audio/mp4": "m4a",
  "video/mp4": "m4a",
  "audio/x-m4a": "m4a",
  "audio/wav": "wav",
  "audio/wave": "wav",
  "audio/x-wav": "wav",
};

/** An upload file name whose extension matches the recorded container. */
export function recordingFileName(mimeType: string) {
  const extension =
    RECORDING_EXTENSIONS[mimeType.split(";")[0].trim().toLowerCase()];
  return extension ? `voice-capture.${extension}` : "voice-capture";
}

function megabytes(bytes: number) {
  const value = bytes / (1024 * 1024);
  return `${value >= 10 ? Math.round(value) : value.toFixed(1)} MB`;
}

function tooLargeMessage(byteSize: number) {
  return byteSize > VOICE_MAX_UPLOAD_BYTES
    ? `This recording is ${megabytes(byteSize)}, over the ${megabytes(VOICE_MAX_UPLOAD_BYTES)} upload limit. Record a shorter note.`
    : `This recording (${megabytes(byteSize)}) is too large to upload. Record a shorter note.`;
}

/** Why a finished recording cannot be uploaded, checked before uploading. */
export function voiceRecordingProblem(byteSize: number) {
  if (byteSize <= 0)
    return "Nothing was recorded. Check your microphone and record again.";
  if (byteSize > VOICE_MAX_UPLOAD_BYTES) return tooLargeMessage(byteSize);
  return undefined;
}

/**
 * Explains a failed upload. `status` is undefined when no response arrived.
 * `retryable` is false when sending the same recording again cannot succeed.
 */
export function voiceUploadError(status: number | undefined, byteSize: number) {
  // A gateway refusing an over-limit body can surface as a 502 or a dropped
  // connection instead of its 413.
  const overLimit =
    byteSize > VOICE_MAX_UPLOAD_BYTES && (status === 502 || !status);
  if (status === 413 || overLimit)
    return { message: tooLargeMessage(byteSize), retryable: false };
  if (status === 400)
    return {
      message:
        "This recording's format was not accepted. Record again, or try another browser.",
      retryable: false,
    };
  if (status === 429)
    return {
      message: "Too many uploads right now. Wait a moment, then retry.",
      retryable: true,
    };
  return {
    message: "Could not upload this recording. Try sending it again.",
    retryable: true,
  };
}
