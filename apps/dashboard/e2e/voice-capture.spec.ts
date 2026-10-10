import { readFileSync } from "node:fs";
import path from "node:path";

import {
  type APIRequestContext,
  expect,
  type Page,
  test,
} from "@playwright/test";

// Voice capture with the browsers' real recorders. Earlier versions injected a
// WAV file in place of MediaRecorder, which hid that the formats browsers
// actually record (WebM, Ogg, MP4) were rejected or failed transcription
// (BUG-7). Here only the microphone is simulated; the browser's own
// MediaRecorder encodes the speech, the Voice UI uploads it, and whisper.cpp
// must transcribe it into an Inbox item.
//
// fixtures/chicken.* hold the spoken phrase "I already bought the chicken."
// (espeak-ng) as 16 kHz WAV and as WebM/Opus, Ogg/Opus and fragmented
// MP4/AAC (ffmpeg); see docs/services/voice-stt.md.

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
const fixtures = path.join(__dirname, "fixtures");
const spokenWav = path.join(fixtures, "chicken.wav");
const transcript = /already bought the chicken/i;
// The phrase lasts 3.5 s; record a little longer so it is captured whole.
const recordingMs = 4_500;

type VoiceJob = {
  id: string;
  status: string;
  transcript?: string;
  errorSummary?: string;
  inboxItemId?: string;
};

// Launch options apply per worker, so they are chosen here by browser.
test.use({
  launchOptions: [
    async ({ browserName }, use) =>
      use(
        browserName === "chromium"
          ? {
              // Chromium's fake microphone plays the spoken WAV once.
              args: [
                "--use-fake-ui-for-media-stream",
                "--use-fake-device-for-media-stream",
                `--use-file-for-fake-audio-capture=${spokenWav}%noloop`,
              ],
            }
          : { firefoxUserPrefs: { "media.autoplay.default": 0 } },
      ),
    { scope: "worker" },
  ],
});

test.describe("Voice capture", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  test.setTimeout(240_000);

  test.describe("in Chromium", () => {
    test.skip(
      ({ browserName }) => browserName !== "chromium",
      "uses Chromium's fake microphone flags",
    );
    test.use({ permissions: ["microphone"] });

    test("a real WebM/Opus recording is transcribed into Inbox", async ({
      page,
      request,
    }) => {
      await recordThroughVoiceUi(page, request, "audio/webm");
    });
  });

  test.describe("in Firefox", () => {
    test.skip(
      ({ browserName }) => browserName !== "firefox",
      "uses a Firefox capture stream as the microphone",
    );
    test("a real Ogg/Opus recording is transcribed into Inbox", async ({
      page,
      request,
    }) => {
      // Trailing silence keeps the stream open until the test presses Stop;
      // a stream that ends would stop the recorder by itself.
      await page.addInitScript(
        speakIntoMicrophone,
        withSilence(readFileSync(spokenWav), 10).toString("base64"),
      );
      await recordThroughVoiceUi(page, request, "audio/ogg");
    });
  });

  // Upload checks without a browser, run once: Safari's MP4 and Firefox's Ogg
  // through the public API, a recording over Fiber's old 4 MiB default body
  // limit, and the refusals.
  test.describe("upload API", () => {
    test.skip(
      ({ browserName }) => browserName !== "chromium",
      "browser-independent; runs in the chromium project only",
    );

    for (const [file, contentType] of [
      ["chicken.ogg", "audio/ogg"],
      ["chicken.m4a", "audio/mp4"],
    ] as const) {
      test(`${file} is sniffed as ${contentType} and transcribed into Inbox`, async ({
        request,
      }) => {
        const job = await uploadAudio(
          request,
          readFileSync(path.join(fixtures, file)),
        );
        await expectCapturedInInbox(request, job, contentType);
      });
    }

    test("a 5 MiB WAV is accepted and transcribed", async ({ request }) => {
      const job = await uploadAudio(
        request,
        paddedWav(readFileSync(spokenWav), 5 * 1024 * 1024),
      );
      await expectCapturedInInbox(request, job, "audio/wave");
    });

    test("non-audio and over-limit uploads are refused", async ({
      request,
    }) => {
      const notAudio = await postAudio(
        request,
        Buffer.from("I already bought the chicken."),
      );
      expect(notAudio.status()).toBe(400);
      expect((await notAudio.json()).error.code).toBe("unsupported_audio");

      // Just over LIFE_OS_VOICE_MAX_UPLOAD_BYTES but within the gateway's
      // 26m, so Voice itself answers.
      const tooLarge = await postAudio(
        request,
        paddedWav(readFileSync(spokenWav), 25 * 1024 * 1024 + 2),
      );
      expect(tooLarge.status()).toBe(413);
      expect((await tooLarge.json()).error.code).toBe("audio_too_large");
    });
  });
});

async function recordThroughVoiceUi(
  page: Page,
  request: APIRequestContext,
  contentType: string,
) {
  await page.goto("/");
  await page
    .getByRole("button", { name: /record|voice|dictate/i })
    .first()
    .click();
  const stop = page.getByRole("button", { name: /stop/i });
  await expect(stop).toBeVisible();
  // Real time: the microphone has to play the whole phrase into the recorder.
  await page.waitForTimeout(recordingMs);
  const upload = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().includes("/api/voice/v1/audio-jobs"),
  );
  await stop.click();
  const created = await upload;
  expect(created.status()).toBe(202);

  await expectCapturedInInbox(
    request,
    (await created.json()) as VoiceJob,
    contentType,
  );
  await expect(page.getByText("Transcribed and added to Inbox.")).toBeVisible({
    timeout: 15_000,
  });
}

/**
 * Firefox's fake media device only produces a tone, and Web Audio needs an
 * audio output device that a headless server lacks. The microphone is
 * therefore an <audio> element playing the spoken WAV, captured as a
 * MediaStream; the MediaRecorder that encodes it is still Firefox's own.
 */
function speakIntoMicrophone(wavBase64: string) {
  Object.defineProperty(navigator, "mediaDevices", {
    configurable: true,
    value: {
      getUserMedia: async () => {
        const wav = Uint8Array.from(atob(wavBase64), (c) => c.charCodeAt(0));
        const audio = new Audio(
          URL.createObjectURL(new Blob([wav], { type: "audio/wav" })),
        );
        const stream = (
          audio as HTMLAudioElement & { mozCaptureStream(): MediaStream }
        ).mozCaptureStream();
        await audio.play();
        return stream;
      },
    },
  });
}

/** Posts audio declared as octet-stream: Voice must sniff the bytes. */
function postAudio(request: APIRequestContext, audio: Buffer) {
  return request.post("/api/voice/v1/audio-jobs", {
    multipart: {
      audio: {
        name: "recording.bin",
        mimeType: "application/octet-stream",
        buffer: audio,
      },
      source: "web",
    },
  });
}

async function uploadAudio(request: APIRequestContext, audio: Buffer) {
  const response = await postAudio(request, audio);
  expect(response.status()).toBe(202);
  return (await response.json()) as VoiceJob;
}

/** Waits for the job to finish and checks the Inbox item it captured. */
async function expectCapturedInInbox(
  request: APIRequestContext,
  created: VoiceJob,
  contentType: string,
) {
  let job = created;
  // Stop at the first state that is not progress, so a failed transcription
  // reports its error instead of waiting out the retries.
  await expect
    .poll(
      async () => {
        const response = await request.get(
          `/api/voice/v1/audio-jobs/${created.id}`,
        );
        job = (await response.json()) as VoiceJob;
        return job.status;
      },
      { timeout: 180_000 },
    )
    .not.toMatch(/^(queued|processing)$/);
  expect(job.status, job.errorSummary).toBe("succeeded");
  expect(job.transcript).toMatch(transcript);

  const response = await request.get(
    `/api/inbox/v1/inbox-items/${job.inboxItemId}`,
  );
  expect(response.ok()).toBeTruthy();
  const item = (await response.json()) as {
    content: string;
    source: string;
    sourceMetadata?: { audioJobId?: string; contentType?: string };
  };
  expect(item.content).toMatch(transcript);
  expect(item.source).toBe("voice");
  expect(item.sourceMetadata?.audioJobId).toBe(created.id);
  // The container Voice sniffed from the uploaded bytes.
  expect(item.sourceMetadata?.contentType).toBe(contentType);
}

/**
 * Grows a WAV to `size` bytes with a JUNK chunk before the samples. Decoders
 * skip it, so the speech (and the transcript) stay the same.
 */
function paddedWav(wav: Buffer, size: number) {
  const fmtEnd = 20 + wav.readUInt32LE(16); // "fmt " chunk at offset 12
  const junk = Buffer.alloc(size - wav.length);
  junk.write("JUNK", 0, "latin1");
  junk.writeUInt32LE(junk.length - 8, 4);
  const padded = Buffer.concat([
    wav.subarray(0, fmtEnd),
    junk,
    wav.subarray(fmtEnd),
  ]);
  padded.writeUInt32LE(padded.length - 8, 4);
  return padded;
}

/** Appends `seconds` of silence to a WAV whose last chunk holds the samples. */
function withSilence(wav: Buffer, seconds: number) {
  const silence = Buffer.alloc(wav.readUInt32LE(28) * seconds); // byte rate
  const longer = Buffer.concat([wav, silence]);
  const data = longer.indexOf("data", 12, "latin1");
  longer.writeUInt32LE(
    longer.readUInt32LE(data + 4) + silence.length,
    data + 4,
  );
  longer.writeUInt32LE(longer.length - 8, 4);
  return longer;
}
