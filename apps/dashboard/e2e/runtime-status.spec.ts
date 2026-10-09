import { execFileSync } from "node:child_process";
import path from "node:path";

import { expect, test, type Page } from "@playwright/test";

// Forces real dependency and worker problems on the disposable verification
// stack and checks that the Dashboard explains each one. Faults are injected
// only through tools/verify-compose, which refuses non-verification resources.
// Run with `make runtime-status-smoke`; it stops services, so it never runs as
// part of the parallel browser suite.
const enabled = Boolean(
  process.env.LIFE_OS_E2E_URL && process.env.LIFE_OS_E2E_FAULTS,
);
const repoRoot = path.resolve(process.cwd(), "../..");

function compose(...args: string[]) {
  execFileSync("./tools/verify-compose", args, {
    cwd: repoRoot,
    stdio: "ignore",
  });
}

async function expectCheck(
  page: Page,
  check: string,
  text: string | RegExp,
  timeout = 120_000,
) {
  await expect(async () => {
    await page.goto("/status");
    await expect(page.getByRole("region", { name: check })).toContainText(
      text,
      { timeout: 1_000 },
    );
  }).toPass({ timeout, intervals: [2_000] });
}

async function expectHomeAlert(page: Page, text: RegExp) {
  await page.goto("/");
  const alert = page.getByRole("link", { name: /^System status:/ });
  await expect(alert).toContainText(text);
  await alert.click();
  await expect(
    page.getByRole("heading", { name: "Is Life OS working?" }),
  ).toBeVisible();
}

/** Half a second of 16 kHz mono silence. */
function silentWav() {
  const samples = 8_000;
  const buffer = Buffer.alloc(44 + samples * 2);
  buffer.write("RIFF", 0);
  buffer.writeUInt32LE(36 + samples * 2, 4);
  buffer.write("WAVEfmt ", 8);
  buffer.writeUInt32LE(16, 16);
  buffer.writeUInt16LE(1, 20);
  buffer.writeUInt16LE(1, 22);
  buffer.writeUInt32LE(16_000, 24);
  buffer.writeUInt32LE(32_000, 28);
  buffer.writeUInt16LE(2, 32);
  buffer.writeUInt16LE(16, 34);
  buffer.write("data", 36);
  buffer.writeUInt32LE(samples * 2, 40);
  return buffer;
}

test.describe("Runtime status under forced problems", () => {
  test.skip(
    !enabled,
    "set LIFE_OS_E2E_URL and LIFE_OS_E2E_FAULTS (make runtime-status-smoke)",
  );
  test.describe.configure({ mode: "serial" });
  test.setTimeout(420_000);

  test("a healthy stack reports everything working and Home stays quiet", async ({
    page,
  }) => {
    await expect(async () => {
      await page.goto("/status");
      await expect(page.getByRole("status")).toHaveText(
        "Everything is working.",
        { timeout: 1_000 },
      );
    }).toPass({ timeout: 120_000, intervals: [2_000] });
    await page.goto("/");
    await expect(
      page.getByRole("link", { name: /^System status:/ }),
    ).toHaveCount(0);
  });

  test("a stopped Whisper is reported on Status and Home", async ({ page }) => {
    compose("stop", "whisper");
    try {
      await expectCheck(
        page,
        "Voice transcription",
        "Whisper speech-to-text is not ready.",
      );
      await expect(
        page.getByRole("region", { name: "Voice transcription" }),
      ).toContainText("Recordings are kept");
      await expectHomeAlert(page, /Whisper speech-to-text is not ready/);
    } finally {
      compose("start", "whisper");
    }
    await expectCheck(page, "Voice transcription", "Idle.", 240_000);
  });

  test("a stopped processor is reported", async ({ page }) => {
    compose("stop", "processor");
    try {
      await expectCheck(
        page,
        "Automatic processing",
        "The Inbox processor is not running.",
      );
      await expectHomeAlert(page, /Inbox processor is not running/);
    } finally {
      compose("start", "processor");
    }
    await expectCheck(
      page,
      "Automatic processing",
      /Waiting for work|Processing normally/,
    );
  });

  test("an MCP outage shows the processor's own error", async ({ page }) => {
    compose("stop", "mcp");
    try {
      await expectCheck(
        page,
        "Automatic processing",
        "The processor cannot read Inbox through MCP.",
      );
    } finally {
      compose("start", "mcp");
    }
    await expectCheck(
      page,
      "Automatic processing",
      /Waiting for work|Processing normally/,
    );
  });

  test("a signed-out Codex is reported with the fix, then recovers", async ({
    page,
  }) => {
    // Remove only the container's tmpfs copy of the credentials; the host
    // session is mounted read-only and untouched. A restart copies it back.
    compose(
      "exec",
      "-T",
      "processor",
      "sh",
      "-c",
      'rm -f "$CODEX_HOME/auth.json"',
    );
    const title = `P8 status signed-out ${Date.now()}`;
    try {
      await page.goto("/");
      await page
        .locator("#inbox-capture")
        .fill(`Create a task titled ${title}.`);
      await page.getByRole("button", { name: "Capture" }).click();
      await expectCheck(
        page,
        "Automatic processing",
        "Codex is not signed in.",
        240_000,
      );
      const processing = page.getByRole("region", {
        name: "Automatic processing",
      });
      await expect(processing).toContainText("codex login");
      await expect(processing).toContainText("401 Unauthorized");
      await expectCheck(page, "Inbox backlog", /failed and is being retried/);
      await expectHomeAlert(page, /Codex is not signed in/);
    } finally {
      compose("restart", "processor");
    }
    await expectCheck(
      page,
      "Automatic processing",
      "Processing normally.",
      240_000,
    );
    await expectCheck(page, "Inbox backlog", "Nothing waiting.");
  });

  test("a stopped Tasks service is named", async ({ page }) => {
    compose("stop", "tasks");
    try {
      await expectCheck(page, "Services", "Tasks is not ready.");
      await expectHomeAlert(page, /Tasks is not ready/);
    } finally {
      compose("start", "tasks");
    }
    await expectCheck(page, "Services", "All services are ready.");
  });

  test("a Voice worker that stops picking up recordings is reported", async ({
    page,
    request,
  }) => {
    compose("stop", "voice-worker");
    try {
      const upload = await request.post("/api/voice/v1/audio-jobs", {
        multipart: {
          audio: {
            name: "silence.wav",
            mimeType: "audio/wav",
            buffer: silentWav(),
          },
          source: "web",
        },
      });
      expect(upload.status()).toBe(202);
      // Due work is stalled once it has waited two minutes with nothing
      // processing.
      await expectCheck(
        page,
        "Voice transcription",
        "The Voice worker is not picking up recordings.",
        240_000,
      );
      await expectHomeAlert(page, /Voice worker is not picking up/);
    } finally {
      compose("start", "voice-worker");
    }
  });
});
