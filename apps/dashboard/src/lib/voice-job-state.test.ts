import assert from "node:assert/strict";
import test from "node:test";

import {
  isVoiceJobTerminal,
  pickRecordingType,
  recordingFileName,
  VOICE_MAX_UPLOAD_BYTES,
  voiceJobMessage,
  voiceRecordingProblem,
  voiceUploadError,
} from "./voice-job-state";

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

test("recording format follows each browser's supported containers", () => {
  const supports =
    (...types: string[]) =>
    (type: string) =>
      types.includes(type);
  // Firefox records Ogg (and WebM); Chromium has no Ogg recorder.
  assert.equal(
    pickRecordingType(
      supports("audio/ogg;codecs=opus", "audio/webm;codecs=opus"),
    ),
    "audio/ogg;codecs=opus",
  );
  assert.equal(
    pickRecordingType(supports("audio/webm;codecs=opus", "audio/mp4")),
    "audio/webm;codecs=opus",
  );
  // Safari.
  assert.equal(pickRecordingType(supports("audio/mp4")), "audio/mp4");
  assert.equal(pickRecordingType(supports()), undefined);
});

test("the upload file name matches the recorded container", () => {
  assert.equal(
    recordingFileName("audio/webm;codecs=opus"),
    "voice-capture.webm",
  );
  assert.equal(
    recordingFileName("audio/ogg; codecs=opus"),
    "voice-capture.ogg",
  );
  assert.equal(
    recordingFileName("audio/mp4;codecs=mp4a.40.2"),
    "voice-capture.m4a",
  );
  assert.equal(recordingFileName("audio/wav"), "voice-capture.wav");
  assert.equal(recordingFileName(""), "voice-capture");
});

test("recordings are size-checked before upload", () => {
  assert.equal(voiceRecordingProblem(1024), undefined);
  assert.equal(voiceRecordingProblem(VOICE_MAX_UPLOAD_BYTES), undefined);
  assert.match(voiceRecordingProblem(0) ?? "", /Nothing was recorded/);
  assert.match(
    voiceRecordingProblem(VOICE_MAX_UPLOAD_BYTES + 1) ?? "",
    /25 MB, over the 25 MB upload limit/,
  );
  assert.match(
    voiceRecordingProblem(30 * 1024 * 1024) ?? "",
    /30 MB, over the 25 MB upload limit/,
  );
});

test("upload failures explain size and format problems", () => {
  const overLimit = VOICE_MAX_UPLOAD_BYTES + 2 * 1024 * 1024;
  assert.deepEqual(voiceUploadError(413, overLimit), {
    message:
      "This recording is 27 MB, over the 25 MB upload limit. Record a shorter note.",
    retryable: false,
  });
  // A server configured below the browser's limit still answers 413.
  assert.match(
    voiceUploadError(413, 5 * 1024 * 1024).message,
    /5\.0 MB\) is too large/,
  );
  // Through Cloudflare the gateway's 413 can arrive as 502 or a reset.
  assert.match(voiceUploadError(502, overLimit).message, /upload limit/);
  assert.match(voiceUploadError(undefined, overLimit).message, /upload limit/);
  assert.equal(voiceUploadError(502, 1024).retryable, true);
  assert.doesNotMatch(voiceUploadError(502, 1024).message, /limit/);
  assert.deepEqual(voiceUploadError(400, 1024).retryable, false);
  assert.match(voiceUploadError(400, 1024).message, /format was not accepted/);
  assert.equal(voiceUploadError(429, 1024).retryable, true);
});
