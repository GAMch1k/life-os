import { expect, test } from "@playwright/test";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
const jfkFixture =
  "https://github.com/ggerganov/whisper.cpp/raw/master/samples/jfk.wav";

test.describe("Dashboard voice capture", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  test.setTimeout(240_000);

  test("records in the browser, uploads through the Voice UI, and renders its final persisted status", async ({
    page,
    request,
  }) => {
    // This remains a browser workflow: the MediaRecorder fixture stands in for
    // microphone hardware, but the VoiceCapture component owns the upload and
    // status polling path. The temporary fixture is deliberately not checked
    // into the repository.
    const fixture = await fetch(jfkFixture);
    expect(fixture.ok).toBeTruthy();
    const bytes = Array.from(new Uint8Array(await fixture.arrayBuffer()));

    await page.addInitScript((audioBytes: number[]) => {
      type FixtureStream = { getTracks: () => Array<{ stop: () => void }> };
      const stream: FixtureStream = {
        getTracks: () => [{ stop: () => undefined }],
      };
      class FixtureMediaRecorder {
        mimeType = "audio/wav";
        ondataavailable?: (event: { data: Blob }) => void;
        onstop?: () => void;

        constructor(readonly stream: FixtureStream) {}
        start() {}
        stop() {
          this.ondataavailable?.({
            data: new Blob([Uint8Array.from(audioBytes).buffer], {
              type: this.mimeType,
            }),
          });
          this.onstop?.();
        }
      }

      Object.defineProperty(navigator, "mediaDevices", {
        configurable: true,
        value: { getUserMedia: async () => stream },
      });
      Object.defineProperty(window, "MediaRecorder", {
        configurable: true,
        value: FixtureMediaRecorder,
      });
    }, bytes);

    await page.goto("/");
    await page.getByRole("button", { name: "Record" }).click();
    const upload = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes("/api/voice/v1/audio-jobs"),
    );
    await page.getByRole("button", { name: "Stop" }).click();
    const created = await upload;
    expect(created.status()).toBe(202);
    const job = (await created.json()) as { id: string };
    expect(job.id).toBeTruthy();

    await expect
      .poll(
        async () => {
          const status = await request.get(
            `/api/voice/v1/audio-jobs/${job.id}`,
          );
          const data = await status.json();
          return data.status;
        },
        { timeout: 180_000 },
      )
      .toBe("succeeded");
    await expect(page.getByText("Transcribed and added to Inbox.")).toBeVisible(
      {
        timeout: 15_000,
      },
    );
  });
});
