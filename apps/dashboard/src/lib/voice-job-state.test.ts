import assert from "node:assert/strict";
import test from "node:test";

import { isVoiceJobTerminal, voiceJobMessage } from "./voice-job-state";

const job = {
  id: "job",
  source: "web",
  attemptCount: 1,
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
} as const;

test("Voice status lifecycle distinguishes retryable and terminal jobs", () => {
  assert.equal(isVoiceJobTerminal("queued"), false);
  assert.equal(isVoiceJobTerminal("retryable"), false);
  assert.equal(isVoiceJobTerminal("succeeded"), true);
  assert.match(
    voiceJobMessage({ ...job, status: "retryable" }),
    /retry automatically/i,
  );
  assert.match(
    voiceJobMessage({
      ...job,
      status: "failed",
      errorSummary: "Whisper unavailable",
    }),
    /Whisper unavailable/,
  );
});
