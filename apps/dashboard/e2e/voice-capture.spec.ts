import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
const jfkFixture =
  "https://github.com/ggerganov/whisper.cpp/raw/master/samples/jfk.wav";
const localFixture = process.env.LIFE_OS_E2E_VOICE_AUDIO_FILE;
const expectedTaskTitle = process.env.LIFE_OS_E2E_EXPECT_VOICE_TASK_TITLE;
const expectedCompletedTaskTitle =
  process.env.LIFE_OS_E2E_EXPECT_VOICE_COMPLETED_TASK_TITLE;

test.describe("Dashboard voice capture", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  test.setTimeout(240_000);

  test("records in the browser, uploads through the Voice UI, and renders its final persisted status", async ({
    page,
    request,
  }) => {
    let completedTaskId: string | undefined;
    if (expectedCompletedTaskTitle) {
      const response = await request.post("/api/tasks/v1/tasks", {
        data: { title: expectedCompletedTaskTitle },
      });
      expect(response.status()).toBe(201);
      completedTaskId = (await response.json()).id;
    }
    // This remains a browser workflow: the MediaRecorder fixture stands in for
    // microphone hardware, but the VoiceCapture component owns the upload and
    // status polling path. The temporary fixture is deliberately not checked
    // into the repository.
    const bytes = localFixture
      ? Array.from(await readFile(localFixture))
      : await downloadFixture();

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

    let finalJob: { status: string; inboxItemId?: string } | undefined;
    await expect
      .poll(
        async () => {
          const status = await request.get(
            `/api/voice/v1/audio-jobs/${job.id}`,
          );
          const data = (await status.json()) as {
            status: string;
            inboxItemId?: string;
          };
          finalJob = data;
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

    if (expectedTaskTitle) {
      expect(finalJob?.inboxItemId).toBeTruthy();
      const inboxItemId = finalJob?.inboxItemId;
      if (!inboxItemId)
        throw new Error("succeeded Voice job has no Inbox item");
      await expect
        .poll(
          async () => {
            const attempts = await request.get(
              `/api/inbox/v1/inbox-items/${inboxItemId}/processing-attempts`,
            );
            const data = await attempts.json();
            return data.items?.[0]?.status;
          },
          { timeout: 150_000 },
        )
        .toBe("processed");
      await expect
        .poll(
          async () => {
            const tasks = await request.get(
              `/api/tasks/v1/tasks?query=${encodeURIComponent(expectedTaskTitle)}`,
            );
            const data = await tasks.json();
            return data.items?.some((entry: { title: string }) =>
              entry.title
                .toLocaleLowerCase()
                .includes(expectedTaskTitle.toLocaleLowerCase()),
            );
          },
          { timeout: 30_000 },
        )
        .toBeTruthy();
    }
    if (expectedCompletedTaskTitle && completedTaskId) {
      await expect
        .poll(
          async () => {
            const response = await request.get(
              `/api/tasks/v1/tasks/${completedTaskId}`,
            );
            return (await response.json()).completed;
          },
          { timeout: 150_000 },
        )
        .toBe(true);
    }
  });
});

async function downloadFixture() {
  const fixture = await fetch(jfkFixture);
  expect(fixture.ok).toBeTruthy();
  return Array.from(new Uint8Array(await fixture.arrayBuffer()));
}
