import { expect, test } from "./fixtures";
import {
  captureHere,
  markDocument,
  requestHumanReview,
  sameDocument,
  unactionable,
} from "./review-setup";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

// BUG-29: the status under the composer said "Needs review", but the page's
// Human Review card or queue only showed the capture after a reload.
test.describe("Human Review appears without a reload", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("Home's Human Review card lists a capture once it needs review", async ({
    page,
    request,
  }) => {
    const content = unactionable(Date.now(), "Home live ");
    await page.goto("/");
    await markDocument(page);
    const id = await captureHere(page, content);
    try {
      await requestHumanReview(request, id);
      await expect(
        page
          .getByRole("list", { name: "Just captured" })
          .getByRole("listitem")
          .filter({ hasText: content }),
      ).toContainText("Needs review", { timeout: 30_000 });
      // The newest capture in review, so it is among the card's previews.
      await expect(page.locator("#attention-review")).toContainText(content, {
        timeout: 15_000,
      });
      expect(await sameDocument(page)).toBe(true);
    } finally {
      await request.post(`/api/inbox/v1/inbox-items/${id}/discard`);
    }
  });

  test("the /inbox queue lists a capture made there once it needs review", async ({
    page,
    request,
  }) => {
    const content = unactionable(Date.now(), "Inbox live ");
    await page.goto("/inbox");
    await markDocument(page);
    const id = await captureHere(page, content);
    try {
      await requestHumanReview(request, id);
      const review = page
        .locator("#human-review section")
        .filter({ hasText: content });
      await expect(review).toBeVisible({ timeout: 30_000 });
      await expect(
        review.getByRole("button", { name: `Discard capture: ${content}` }),
      ).toBeVisible();
      expect(await sameDocument(page)).toBe(true);
    } finally {
      await request.post(`/api/inbox/v1/inbox-items/${id}/discard`);
    }
  });
});
