import { expect, test } from "./fixtures";

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
