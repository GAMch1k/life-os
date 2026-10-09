import { expect, test } from "@playwright/test";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

test.describe("Human Review without candidates", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("a capture Codex can do nothing with can be discarded", async ({
    page,
    request,
  }) => {
    // A real capture through the automatic processor and Codex: a quotation
    // is not a request, so it lands in Human Review with no candidates.
    const quote = `Ask not what your country can do for you (${Date.now()}).`;
    await page.goto("/");
    await page.locator("#inbox-capture").fill(quote);
    const created = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes("/api/inbox/v1/inbox-items"),
    );
    await page.getByRole("button", { name: "Capture" }).click();
    const id = ((await (await created).json()) as { id: string }).id;
    await expect
      .poll(
        async () =>
          (await (await request.get(`/api/inbox/v1/inbox-items/${id}`)).json())
            .state,
        { timeout: 180_000 },
      )
      .toBe("human_review");

    await page.goto("/inbox");
    const review = page.locator("#human-review section").filter({
      hasText: quote,
    });
    await expect(review).toContainText(
      "Life OS found nothing it can safely do with this capture.",
    );
    await review
      .getByRole("button", { name: `Discard capture: ${quote}` })
      .click();
    await expect(
      page.locator("#human-review section").filter({ hasText: quote }),
    ).toHaveCount(0);

    const item = await (
      await request.get(`/api/inbox/v1/inbox-items/${id}`)
    ).json();
    expect(item.state).toBe("discarded");
    const unresolved = await (
      await request.get("/api/inbox/v1/inbox-items?unresolved=true&limit=100")
    ).json();
    expect(
      unresolved.items.some((entry: { id: string }) => entry.id === id),
    ).toBe(false);
    await page.reload();
    await expect(page.getByText(quote)).toHaveCount(0);
  });
});
