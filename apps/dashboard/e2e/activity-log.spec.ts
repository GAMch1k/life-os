import {
  addDays,
  berlinDay,
  createSource,
  deleteSource,
  failingFeedUrl,
  FEED_OVERLAY_HINT,
  feedDir,
  refreshSource,
  waitForRefresh,
  writeFeed,
} from "./calendar-feed";
import { type APIRequestContext, expect, type Page, test } from "./fixtures";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

test.describe("Event Log", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("follows a real capture through the agent to the Task and its later completion", async ({
    page,
    request,
  }) => {
    const title = `P8 activity trace ${Date.now()}`;

    // A normal Dashboard capture, processed by the automatic processor with
    // the authenticated Codex CLI, which creates the Task through MCP.
    await page.goto("/");
    await page.locator("#inbox-capture").fill(`Create a task titled ${title}.`);
    await page.getByRole("button", { name: "Capture" }).click();
    await expect(page.getByText(title)).toBeVisible();
    let task: { id: string; title: string } | undefined;
    await expect
      .poll(
        async () => {
          const tasks = await request.get(
            `/api/tasks/v1/tasks?query=${encodeURIComponent(title)}`,
          );
          task = (await tasks.json()).items?.find((entry: { title: string }) =>
            entry.title.startsWith(title),
          );
          return Boolean(task);
        },
        { timeout: 150_000 },
      )
      .toBeTruthy();

    // Later, the user completes it in the Tasks workspace.
    await page.goto("/tasks");
    await page.getByRole("button", { name: `Complete ${task!.title}` }).click();
    await expect
      .poll(async () => {
        const response = await request.get(`/api/tasks/v1/tasks/${task!.id}`);
        return (await response.json()).completed;
      })
      .toBe(true);

    // The capture appears in the Event Log; filter to captures and open it.
    await page.goto("/");
    await page.getByRole("link", { name: "Activity" }).first().click();
    await expect(
      page.getByRole("heading", { name: "Event Log", level: 1 }),
    ).toBeVisible();
    await page
      .getByRole("combobox", { name: "Event", exact: true })
      .selectOption("inbox.item.captured");
    await page.getByRole("button", { name: "Apply" }).click();
    await expect(page).toHaveURL(/type=inbox\.item\.captured/);
    const entries = page.getByRole("list", { name: "Activity entries" });
    await entries.getByRole("link").filter({ hasText: title }).click();

    const selected = page.getByRole("region", { name: "Selected event" });
    await expect(selected.getByText("Inbox capture").first()).toBeVisible();
    const downstream = selected
      .locator("section")
      .filter({ has: page.getByRole("heading", { name: "What it led to" }) });
    for (const step of [
      "Processing started",
      "Agent action requested: task_create",
      "Task created",
      "Agent action completed: task_create",
      "Processing completed",
      "Inbox item resolved",
    ]) {
      await expect(downstream.getByText(step, { exact: true })).toBeVisible();
    }

    // Follow the chain to the Task the agent created.
    await downstream.getByRole("link", { name: /^Task created/ }).click();
    await expect(page).toHaveURL(/event=/);
    const upstream = selected
      .locator("section")
      .filter({ has: page.getByRole("heading", { name: "What led here" }) });
    for (const step of [
      "Inbox capture",
      "Processing started",
      "Agent action requested: task_create",
    ]) {
      await expect(upstream.getByText(step, { exact: true })).toBeVisible();
    }

    // The same Task, completed later by the user in a separate workflow.
    const related = selected.locator("section").filter({
      has: page.getByRole("heading", {
        name: "Same task in other workflows",
      }),
    });
    await expect(related.getByText(/by You/)).toBeVisible();
    await related.getByRole("link", { name: /^Task completed/ }).click();
    await expect(
      selected.getByText("Task completed", { exact: true }).first(),
    ).toBeVisible();
    await selected.getByText("Diagnostics").click();
    await expect(selected.getByText(`task/${task!.id}`)).toBeVisible();
  });

  test("expands a shortened summary, copies diagnostics, and scrolls the entry on its own", async ({
    context,
    page,
    request,
  }) => {
    // A Human Review request whose reason is far longer than its summary
    // keeps: Inbox cuts the summary to 120 characters and keeps the full
    // reason in the event's data.
    const stamp = Date.now();
    const reason = `Two Tasks match P8 log ${stamp}. ${"The processor could not tell which one was meant, so it asks before changing anything. ".repeat(8)}End of reason ${stamp}.`;
    const item = await request.post("/api/inbox/v1/inbox-items", {
      data: { content: `Complete P8 log ${stamp}`, source: "manual" },
    });
    expect(item.status()).toBe(201);
    const inbox = await item.json();
    try {
      const attempt = await request.post(
        `/api/inbox/v1/inbox-items/${inbox.id}/processing-attempts`,
        {
          data: {
            idempotencyKey: `log-${stamp}`,
            agentName: "e2e",
            modelProfile: "e2e",
          },
        },
      );
      expect(attempt.status()).toBe(201);
      const review = await request.post(
        `/api/inbox/v1/inbox-items/${inbox.id}/human-review`,
        {
          data: {
            attemptId: (await attempt.json()).id,
            proposedAction: {
              tool: "task_complete",
              arguments: { taskQuery: `P8 log ${stamp}` },
            },
            candidates: [],
            errorSummary: reason,
          },
        },
      );
      expect(review.ok()).toBeTruthy();
      let eventId: string | undefined;
      await expect
        .poll(async () => {
          const events = await request.get(
            `/api/activity/v1/activity-events?subjectType=inbox_item&subjectId=${inbox.id}&type=inbox.human_review.requested`,
          );
          eventId = (await events.json()).items?.[0]?.id;
          return Boolean(eventId);
        })
        .toBeTruthy();

      await context.grantPermissions(["clipboard-read", "clipboard-write"]);
      await page.setViewportSize({ width: 1280, height: 700 });
      await page.goto(`/activity?event=${eventId}`);
      const selected = page.getByRole("region", { name: "Selected event" });
      await expect(
        selected.getByText("Human Review requested").first(),
      ).toBeVisible();

      // The summary is shortened by default and expands to the whole reason.
      const more = selected.getByRole("button", { name: "Show more" });
      await expect(more).toHaveAttribute("aria-expanded", "false");
      const summary = page.locator(
        `[id="${await more.getAttribute("aria-controls")}"]`,
      );
      await expect(summary).toContainText(`Two Tasks match P8 log ${stamp}.`);
      await expect(summary).not.toContainText(`End of reason ${stamp}.`);
      await more.click();
      const less = selected.getByRole("button", { name: "Show less" });
      await expect(less).toHaveAttribute("aria-expanded", "true");
      await expect(summary).toHaveText(`Needs your decision: ${reason}`);
      await less.click();
      await expect(more).toHaveAttribute("aria-expanded", "false");
      await expect(summary).not.toContainText(`End of reason ${stamp}.`);
      await more.click();

      // On a laptop screen the tall entry scrolls by itself and stays fully
      // in view below the 48px header, wherever the page is scrolled.
      await selected.getByText("Diagnostics", { exact: true }).click();
      const pane = () =>
        selected.evaluate((element) => {
          const box = element.getBoundingClientRect();
          return {
            overflowY: getComputedStyle(element).overflowY,
            scrollable: element.scrollHeight > element.clientHeight,
            top: Math.round(box.top),
            bottom: Math.round(box.bottom),
          };
        });
      for (const end of ["bottom", "top"]) {
        await page.evaluate(
          (y) => window.scrollTo(0, y),
          end === "top" ? 0 : 1_000_000,
        );
        await expect
          .poll(pane)
          .toMatchObject({ overflowY: "auto", scrollable: true });
        await expect.poll(async () => (await pane()).top).toBeGreaterThan(48);
        await expect
          .poll(async () => (await pane()).bottom)
          .toBeLessThanOrEqual(700);
      }
      // Scrolling over it moves only the entry, not the page or the list.
      await selected.evaluate((element) => element.scrollTo(0, 0));
      await selected
        .getByRole("heading", { name: "What led here", exact: true })
        .hover();
      const paneScroll = await selected.evaluate(
        (element) => element.scrollTop,
      );
      const pageScroll = await page.evaluate(() => window.scrollY);
      await page.mouse.wheel(0, 3000);
      await expect
        .poll(() => selected.evaluate((element) => element.scrollTop))
        .toBeGreaterThan(paneScroll);
      expect(await page.evaluate(() => window.scrollY)).toBe(pageScroll);

      // Diagnostics copies exactly the event data it shows.
      await selected.getByRole("button", { name: "Copy diagnostics" }).click();
      await expect(selected.getByRole("status")).toHaveText("Copied");
      const copied = await page.evaluate(() => navigator.clipboard.readText());
      expect(copied).toBe(await selected.locator("pre").textContent());
      expect(JSON.parse(copied).reason).toBe(reason);
    } finally {
      await request.post(`/api/inbox/v1/inbox-items/${inbox.id}/discard`);
    }
  });

  test("explains an empty agent domain and fits a phone screen", async ({
    page,
  }) => {
    // A day long before the Event Log existed: nothing can match it.
    await page.goto("/activity");
    await page
      .getByRole("combobox", { name: "Domain" })
      .selectOption({ label: "Agent changes" });
    await page.getByLabel("From", { exact: true }).fill("2000-01-01");
    await page.getByLabel("To", { exact: true }).fill("2000-01-01");
    await page.getByRole("button", { name: "Apply" }).click();
    await expect(page).toHaveURL(/source=mcp/);

    // The domain says what it records and why it is empty, and points to
    // where the agent's decisions are.
    const events = page.getByRole("region", { name: "Events" });
    await expect(
      events.getByText(/Changes the AI made through MCP/),
    ).toBeVisible();
    await expect(
      events.getByText("The agent hasn't changed anything in this range."),
    ).toBeVisible();
    await expect(
      events.getByRole("link", { name: "Show Inbox activity" }),
    ).toHaveAttribute("href", /source=inbox&fromDate=2000-01-01/);
    await expect(
      page.getByText("No activity matches these filters."),
    ).toHaveCount(0);

    // No horizontal scrolling on a small phone: the filters wrap, and long
    // summaries and tool names break instead of widening the page.
    const overflow = () =>
      page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
    await page.setViewportSize({ width: 360, height: 780 });
    expect(await overflow()).toBeLessThanOrEqual(0);
    await page.goto("/activity");
    await expect(
      page.getByRole("heading", { name: "Event Log", level: 1 }),
    ).toBeVisible();
    expect(await overflow()).toBeLessThanOrEqual(0);
    const entries = page
      .getByRole("list", { name: "Activity entries" })
      .getByRole("link");
    if ((await entries.count()) > 0) {
      await entries.first().click();
      await expect(page).toHaveURL(/event=/);
      await expect(
        page
          .getByRole("region", { name: "Selected event" })
          .getByRole("heading", { name: "Whole workflow" }),
      ).toBeVisible();
      expect(await overflow()).toBeLessThanOrEqual(0);
    }
  });
});

/** Waits until Activity has recorded an event of `type` for the subject. */
async function recorded(
  request: APIRequestContext,
  subjectType: string,
  subjectId: string,
  type: string,
) {
  await expect
    .poll(
      async () => {
        const response = await request.get(
          `/api/activity/v1/activity-events?subjectType=${subjectType}&subjectId=${subjectId}&type=${type}`,
        );
        return ((await response.json()).items ?? []).length;
      },
      { timeout: 30_000 },
    )
    .toBeGreaterThan(0);
}

/** The Event Log filtered to the Calendar domain through its form. */
async function openCalendarDomain(page: Page) {
  await page.goto("/activity");
  await page
    .getByRole("combobox", { name: "Domain" })
    .selectOption({ label: "Calendar" });
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(page).toHaveURL(/source=calendar/);
  return page.getByRole("list", { name: "Activity entries" });
}

test.describe("Event Log: Calendar", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("an event you edit in the Calendar shows as yours, apart from the tool that created it", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const title = `Activity dentist ${stamp}`;
    const moved = `${title} moved`;
    const day = addDays(berlinDay(new Date()), 5);
    const next = addDays(day, 1);
    // 08:00 UTC is 09:00 or 10:00 in Berlin, the same day.
    const created = await request.post("/api/calendar/v1/events", {
      data: {
        title,
        allDay: false,
        startAt: `${day}T08:00:00Z`,
        endAt: `${day}T08:45:00Z`,
      },
    });
    expect(created.status()).toBe(201);
    const event = (await created.json()) as { id: string };
    try {
      // The owner renames and moves it in the Calendar workspace.
      await page.goto(`/calendar?view=week&date=${day}`);
      const events = page.getByRole("region", { name: "Calendar events" });
      await expect(events).toHaveAttribute("aria-busy", "false");
      await events.locator(".fc-event", { hasText: title }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("button", { name: "Edit", exact: true }).click();
      await dialog.getByLabel("Title").fill(moved);
      await dialog.getByLabel("Start date").fill(next);
      await dialog.getByRole("button", { name: "Save" }).click();
      await expect(dialog.getByRole("heading", { name: moved })).toBeVisible();
      await page.keyboard.press("Escape");
      await recorded(
        request,
        "calendar_event",
        event.id,
        "calendar.event.rescheduled",
      );

      // In the Calendar domain of the Event Log, the change is the owner's.
      const entries = await openCalendarDomain(page);
      const entry = entries
        .getByRole("link")
        .filter({ hasText: `Event rescheduled: ${moved}` });
      await expect(entry).toContainText(`Event “${moved}” moved to`);
      await expect(entry).toContainText("Calendar · You");
      await entry.click();

      // The creation by the development tool is the same event's history.
      const selected = page.getByRole("region", { name: "Selected event" });
      const related = selected.locator("section").filter({
        has: page.getByRole("heading", {
          name: "Same calendar event in other workflows",
        }),
      });
      await expect(
        related.getByRole("link", { name: /^Event created: / }),
      ).toContainText("by Developer tool (e2e)");

      // The entry opens the Calendar at the event's new day.
      await selected.getByRole("link", { name: "Open in Calendar" }).click();
      await expect(page).toHaveURL(new RegExp(`/calendar\\?date=${next}$`));
      await expect(events).toHaveAttribute("aria-busy", "false");
      await expect(
        events.locator(".fc-event", { hasText: moved }),
      ).toBeVisible();
    } finally {
      await request.delete(`/api/calendar/v1/events/${event.id}`);
    }
  });

  test("a failed feed refresh shows as the feed's own entry", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const name = `E2E failing feed ${stamp}`;
    const source = await createSource(request, name, failingFeedUrl(stamp));
    try {
      const failed = await waitForRefresh(request, source.id, "failed");
      expect(failed.refresh.lastError).toBeTruthy();
      await recorded(
        request,
        "calendar_source",
        source.id,
        "calendar.source.refresh_failed",
      );

      const entries = await openCalendarDomain(page);
      const entry = entries
        .getByRole("link")
        .filter({ hasText: `Source refresh failed: ${name}` });
      await expect(entry).toContainText(
        `Calendar feed (e2e-failing-feed-${stamp})`,
      );
      await expect(entry).toContainText(
        `Refreshing ${name} failed: ${failed.refresh.lastError}`,
      );
      await entry.click();
      const selected = page.getByRole("region", { name: "Selected event" });
      await expect(
        selected.getByRole("link", { name: "Open Calendar" }),
      ).toHaveAttribute("href", "/calendar");
      // The feed's URL is never recorded.
      await selected.getByText("Diagnostics", { exact: true }).click();
      await expect(selected.locator("pre")).not.toContainText("feed.invalid/");
    } finally {
      await deleteSource(request, source.id);
    }
  });

  test("each feed refresh is one entry that says what it changed", async ({
    page,
    request,
  }) => {
    test.skip(!feedDir, FEED_OVERLAY_HINT);
    const stamp = Date.now();
    const name = `E2E feed ${stamp}`;
    const file = `activity-${stamp}.ics`;
    const day = addDays(berlinDay(new Date()), 3);
    const inAWeek = new Date(Date.now() + 7 * 86_400_000);
    const url = writeFeed(file, [
      { uid: `pickup-${stamp}`, title: `Feed pickup ${stamp}`, day },
      {
        uid: `meeting-${stamp}`,
        title: `Feed meeting ${stamp}`,
        start: inAWeek,
        end: new Date(inAWeek.getTime() + 3_600_000),
      },
    ]);
    const source = await createSource(request, name, url);
    try {
      const first = await waitForRefresh(request, source.id, "imported");
      expect(first.refresh.lastCounts?.inserted).toBe(2);

      // The pickup leaves the feed and another event joins it.
      writeFeed(file, [
        {
          uid: `meeting-${stamp}`,
          title: `Feed meeting ${stamp}`,
          start: inAWeek,
          end: new Date(inAWeek.getTime() + 3_600_000),
        },
        {
          uid: `other-${stamp}`,
          title: `Feed other ${stamp}`,
          day: addDays(day, 1),
        },
      ]);
      const second = await refreshSource(request, source.id);
      expect(second.refresh.lastCounts).toMatchObject({
        inserted: 1,
        removed: 1,
      });

      const entries = await openCalendarDomain(page);
      const refreshes = entries
        .getByRole("link")
        .filter({ hasText: `Source refreshed: ${name}` });
      await expect(refreshes).toHaveCount(2);
      await expect(
        refreshes.filter({ hasText: `${name} — 1 added, 1 cancelled` }),
      ).toContainText(`Calendar feed (e2e-feed-${stamp})`);
      await expect(
        refreshes.filter({ hasText: `${name} — 2 added` }),
      ).toBeVisible();
      // Imported events are summarized by their refresh, not listed one by
      // one.
      await expect(
        entries.getByRole("link").filter({ hasText: `Feed pickup ${stamp}` }),
      ).toHaveCount(0);
    } finally {
      await deleteSource(request, source.id);
    }
  });
});
