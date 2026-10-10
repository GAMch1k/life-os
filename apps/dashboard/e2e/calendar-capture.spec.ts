import { expect, test } from "./fixtures";

const enabled = Boolean(
  process.env.LIFE_OS_E2E_URL && process.env.LIFE_OS_E2E_LIVE_CALENDAR,
);

test.describe("Dashboard Calendar capture", () => {
  test.skip(
    !enabled,
    "set LIFE_OS_E2E_URL and LIFE_OS_E2E_LIVE_CALENDAR for deployed acceptance",
  );

  test("captures a calendar request in the browser and persists exactly one event", async ({
    page,
    request,
  }) => {
    const title = `P7 live calendar acceptance ${Date.now()}`;
    const content = `Create an all-day calendar event titled ${title} on 2026-10-12.`;
    await page.goto("/");
    await page.locator("#inbox-capture").fill(content);
    await page.getByRole("button", { name: "Capture" }).click();

    const inbox = await request.get(
      "/api/inbox/v1/inbox-items?query=state=raw&limit=100",
    );
    expect(inbox.ok()).toBeTruthy();
    const item = (await inbox.json()).items.find(
      (entry: { content: string }) => entry.content === content,
    );
    expect(item).toBeTruthy();

    await expect
      .poll(
        async () => {
          const attempts = await request.get(
            `/api/inbox/v1/inbox-items/${item.id}/processing-attempts`,
          );
          const data = await attempts.json();
          return data.items?.[0]?.status;
        },
        { timeout: 150_000 },
      )
      .toBe("processed");

    const events = await request.get(
      "/api/calendar/v1/events?from=2026-10-12T00:00:00Z&to=2026-10-13T00:00:00Z",
    );
    expect(events.ok()).toBeTruthy();
    const matches = (await events.json()).items.filter(
      (event: { title: string }) => event.title === title,
    );
    expect(matches).toHaveLength(1);
    const deletion = await request.delete(
      `/api/calendar/v1/events/${matches[0].id}`,
    );
    expect(deletion.status()).toBe(204);
  });
});
