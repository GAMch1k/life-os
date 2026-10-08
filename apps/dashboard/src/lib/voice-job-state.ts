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
