import { type APIRequestContext, expect, type Page } from "./fixtures";

// Helpers for specs that need a capture made through the page to wait in
// Human Review. The capture goes through the composer like the owner's; the
// spec then requests the review itself, as the processor does
// (human-review.spec.ts, home-attention.spec.ts), instead of waiting for
// Codex to decide.

/**
 * A capture the processor cannot act on. The processor polls every second
 * and may claim a capture before the spec requests its review; Codex then
 * sends a quotation to Human Review too (human-review-dismiss.spec.ts).
 */
export function unactionable(stamp: number, label = "") {
  return `Ask not what your country can do for you (${label}${stamp}).`;
}

/** The Inbox create request, not a discard or review action on an item. */
export function isInboxCreate(method: string, url: string) {
  return (
    method === "POST" && new URL(url).pathname === "/api/inbox/v1/inbox-items"
  );
}

/** Captures `text` with the page's composer; returns the new item's id. */
export async function captureHere(page: Page, text: string) {
  await page.locator("#inbox-capture").fill(text);
  const created = page.waitForResponse((response) =>
    isInboxCreate(response.request().method(), response.url()),
  );
  // Exact: on /inbox, "Discard capture: …" also contains "capture".
  await page.getByRole("button", { name: "Capture", exact: true }).click();
  const response = await created;
  expect(response.status()).toBe(201);
  return ((await response.json()) as { id: string }).id;
}

/** Moves a capture into Human Review with no candidates. */
export async function requestHumanReview(
  request: APIRequestContext,
  itemId: string,
) {
  const attempt = await request.post(
    `/api/inbox/v1/inbox-items/${itemId}/processing-attempts`,
    {
      data: {
        idempotencyKey: `e2e-review-${itemId}`,
        agentName: "e2e",
        modelProfile: "e2e",
      },
    },
  );
  expect(attempt.status()).toBe(201);
  const started = await attempt.json();
  const review = await request.post(
    `/api/inbox/v1/inbox-items/${itemId}/human-review`,
    {
      data: {
        attemptId: started.id,
        proposedAction: { tool: "none", arguments: {} },
        candidates: [],
        errorSummary: "Nothing safe to do with this capture.",
      },
    },
  );
  expect(review.ok()).toBeTruthy();
}

export async function inboxItem(request: APIRequestContext, itemId: string) {
  return (await (
    await request.get(`/api/inbox/v1/inbox-items/${itemId}`)
  ).json()) as {
    id: string;
    content: string;
    source: string;
    state: string;
    sourceMetadata?: Record<string, unknown>;
  };
}

/**
 * Marks the loaded document; `sameDocument` is false once the page has
 * reloaded or navigated to a new document, which drops the mark.
 */
export async function markDocument(page: Page) {
  await page.evaluate(() => {
    (window as { lifeOsE2eMark?: boolean }).lifeOsE2eMark = true;
  });
}

export function sameDocument(page: Page) {
  return page.evaluate(
    () => (window as { lifeOsE2eMark?: boolean }).lifeOsE2eMark === true,
  );
}
