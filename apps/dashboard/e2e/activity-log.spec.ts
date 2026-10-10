import { expect, test } from "@playwright/test";

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
