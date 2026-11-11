import path from "node:path";

import { expect, type Page, test } from "./fixtures";
import {
  captureInReview,
  inboxItem,
  isInboxCreate,
  unactionable,
} from "./review-setup";

// POL-09: Edit on a Human Review card opens the capture composer with the
// capture's text. Capture sends the edit through the normal capture path (a
// new Inbox item, or a recording for Voice) and only then discards the
// original; Cancel leaves it in review.

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
const spokenWav = path.join(__dirname, "fixtures", "chicken.wav");
// The phrase lasts 3.5 s; record a little longer so it is captured whole.
const recordingMs = 4_500;

// The voice test records through Chromium's fake microphone, which plays
// the spoken fixture once ("I already bought the chicken.").
test.use({
  launchOptions: {
    args: [
      "--use-fake-ui-for-media-stream",
      "--use-fake-device-for-media-stream",
      `--use-file-for-fake-audio-capture=${spokenWav}%noloop`,
    ],
  },
  permissions: ["microphone"],
});

function reviewCard(page: Page, content: string) {
  return page.locator("#human-review section").filter({ hasText: content });
}

function isDiscardOf(itemId: string, method: string, url: string) {
  return (
    method === "POST" &&
    new URL(url).pathname === `/api/inbox/v1/inbox-items/${itemId}/discard`
  );
}

test.describe("Edit a capture from Human Review", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("Capture sends the edited text as a new capture, then discards the original", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const original = unactionable(stamp, "edit original ");
    const edited = `Ask not what you can do for your country (edited ${stamp}).`;
    const id = await captureInReview(request, original);
    let replacementId: string | undefined;
    try {
      const writes: string[] = [];
      page.on("request", (sent) => {
        if (sent.method() === "POST" && sent.url().includes("/api/inbox/")) {
          writes.push(new URL(sent.url()).pathname);
        }
      });
      await page.goto("/inbox");
      const card = reviewCard(page, original);
      await card
        .getByRole("button", { name: `Edit capture: ${original}` })
        .click();
      const field = card.getByRole("textbox", { name: "Edited capture" });
      await expect(field).toBeFocused();
      await expect(field).toHaveValue(original);
      await field.fill(edited);

      const created = page.waitForResponse((response) =>
        isInboxCreate(response.request().method(), response.url()),
      );
      const discarded = page.waitForResponse((response) =>
        isDiscardOf(id, response.request().method(), response.url()),
      );
      await card.getByRole("button", { name: "Capture", exact: true }).click();
      const response = await created;
      expect(response.status()).toBe(201);
      replacementId = ((await response.json()) as { id: string }).id;
      expect((await discarded).status()).toBe(200);
      // The new capture first; the original only once it was accepted.
      expect(writes).toEqual([
        "/api/inbox/v1/inbox-items",
        `/api/inbox/v1/inbox-items/${id}/discard`,
      ]);

      await expect(reviewCard(page, original)).toHaveCount(0);
      await expect(
        page.getByText("Captured your edit; the original was discarded."),
      ).toBeVisible();
      // Followed under the composer like any capture made on the page.
      await expect(
        page
          .getByRole("list", { name: "Just captured" })
          .getByRole("listitem")
          .filter({ hasText: edited }),
      ).toBeVisible();

      expect((await inboxItem(request, id)).state).toBe("discarded");
      const replacement = await inboxItem(request, replacementId);
      expect(replacement.content).toBe(edited);
      expect(replacement.source).toBe("manual");
      expect(replacement.sourceMetadata).toMatchObject({
        replacesItemId: id,
        timeZone: "Europe/Berlin",
      });
    } finally {
      await request.post(`/api/inbox/v1/inbox-items/${id}/discard`);
      if (replacementId) {
        await request.post(
          `/api/inbox/v1/inbox-items/${replacementId}/discard`,
        );
      }
    }
  });

  test("Cancel (or Escape) closes the editor and leaves the capture in review", async ({
    page,
    request,
  }) => {
    const original = unactionable(Date.now(), "edit cancel ");
    const id = await captureInReview(request, original);
    try {
      const writes: string[] = [];
      page.on("request", (sent) => {
        if (
          sent.method() !== "GET" &&
          /\/api\/(inbox|voice)\//.test(sent.url())
        ) {
          writes.push(`${sent.method()} ${new URL(sent.url()).pathname}`);
        }
      });
      await page.goto("/inbox");
      const card = reviewCard(page, original);
      const edit = card.getByRole("button", {
        name: `Edit capture: ${original}`,
      });
      const field = card.getByRole("textbox", { name: "Edited capture" });

      await edit.click();
      await field.fill(`${original} With more detail.`);
      await card.getByRole("button", { name: "Cancel" }).click();
      await expect(field).toHaveCount(0);
      await expect(edit).toBeFocused();

      // A new edit starts from the capture's text again; Escape cancels too.
      await edit.click();
      await expect(field).toHaveValue(original);
      await field.press("Escape");
      await expect(field).toHaveCount(0);
      await expect(
        card.getByRole("button", { name: `Discard capture: ${original}` }),
      ).toBeVisible();

      expect(writes).toEqual([]);
      expect((await inboxItem(request, id)).state).toBe("human_review");
    } finally {
      await request.post(`/api/inbox/v1/inbox-items/${id}/discard`);
    }
  });

  test("Record captures the edit by voice; its Inbox item names the original @voice", async ({
    page,
    request,
  }) => {
    test.setTimeout(240_000);
    const original = unactionable(Date.now(), "edit voice ");
    const id = await captureInReview(request, original);
    let replacementId: string | undefined;
    try {
      await page.goto("/inbox");
      const card = reviewCard(page, original);
      await card
        .getByRole("button", { name: `Edit capture: ${original}` })
        .click();
      await card.getByRole("button", { name: "Record" }).click();
      const stop = card.getByRole("button", { name: "Stop" });
      await expect(stop).toBeVisible();
      // Real time: the microphone has to play the whole phrase.
      await page.waitForTimeout(recordingMs);
      const upload = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          response.url().includes("/api/voice/v1/audio-jobs"),
      );
      const discarded = page.waitForResponse((response) =>
        isDiscardOf(id, response.request().method(), response.url()),
      );
      await stop.click();
      const uploaded = await upload;
      expect(uploaded.status()).toBe(202);
      const job = (await uploaded.json()) as { id: string };
      expect((await discarded).status()).toBe(200);
      await expect(reviewCard(page, original)).toHaveCount(0);
      await expect(
        page.getByText(
          "Recording sent; it becomes a new capture once transcribed. The original was discarded.",
        ),
      ).toBeVisible();

      // Voice passes the upload's metadata on to the Inbox item it creates.
      await expect
        .poll(
          async () => {
            const response = await request.get(
              `/api/voice/v1/audio-jobs/${job.id}`,
            );
            replacementId = (
              (await response.json()) as {
                inboxItemId?: string;
              }
            ).inboxItemId;
            return replacementId;
          },
          { timeout: 180_000 },
        )
        .toBeTruthy();
      const replacement = await inboxItem(request, replacementId!);
      expect(replacement.source).toBe("voice");
      expect(replacement.content).toMatch(/already bought the chicken/i);
      expect(replacement.sourceMetadata).toMatchObject({
        replacesItemId: id,
        timeZone: "Europe/Berlin",
        audioJobId: job.id,
      });
      expect((await inboxItem(request, id)).state).toBe("discarded");
    } finally {
      await request.post(`/api/inbox/v1/inbox-items/${id}/discard`);
      if (replacementId) {
        await request.post(
          `/api/inbox/v1/inbox-items/${replacementId}/discard`,
        );
      }
    }
  });
});
