import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";

import {
  expect,
  test,
  type APIRequestContext,
  type Page,
} from "@playwright/test";

// Phase 8 sign-off: ten acceptance chains against the integrated product on
// the disposable verification stack, through the normal user triggers
// (Dashboard / Voice → Inbox → automatic processor → authenticated Codex CLI →
// MCP → owning service). Run with `make phase8-acceptance`, which prepares the
// spoken fixture, the Codex CLI wrapper used to force a provider usage limit
// in chain 8, and the stack.
const enabled = Boolean(
  process.env.LIFE_OS_E2E_URL && process.env.LIFE_OS_E2E_PHASE8,
);
const repoRoot = path.resolve(process.cwd(), "../..");
const stamp = Date.now();

type Task = {
  id: string;
  title: string;
  dueAt?: string | null;
  completed: boolean;
};
type InboxItem = {
  id: string;
  state: string;
  content: string;
  nextRetryAt?: string | null;
  retryReason?: string | null;
};
type ActivityEvent = {
  type: string;
  source: string;
  actor: { kind: string; name?: string };
  correlationId: string;
};

function compose(...args: string[]) {
  execFileSync("./tools/verify-compose", args, {
    cwd: repoRoot,
    stdio: "ignore",
  });
}

function simulate(mode: string, until: number) {
  const dir = process.env.LIFE_OS_SIM_DIR!;
  writeFileSync(path.join(dir, "mode"), `${mode}\n`);
  writeFileSync(path.join(dir, "until"), `${until}\n`);
}

async function tasksTitled(request: APIRequestContext, title: string) {
  const response = await request.get(
    `/api/tasks/v1/tasks?query=${encodeURIComponent(title)}&limit=100`,
  );
  const items = ((await response.json()).items ?? []) as Task[];
  return items.filter((task) => task.title.startsWith(title));
}

async function getTask(request: APIRequestContext, id: string) {
  return (await (
    await request.get(`/api/tasks/v1/tasks/${id}`)
  ).json()) as Task;
}

async function createTask(request: APIRequestContext, title: string) {
  const response = await request.post("/api/tasks/v1/tasks", {
    data: { title },
  });
  expect(response.status()).toBe(201);
  return (await response.json()) as Task;
}

async function item(request: APIRequestContext, id: string) {
  return (await (
    await request.get(`/api/inbox/v1/inbox-items/${id}`)
  ).json()) as InboxItem;
}

/** Capture text on Home like the user does, and return the Inbox item. */
async function captureOnHome(
  page: Page,
  request: APIRequestContext,
  text: string,
) {
  await page.goto("/");
  await page.locator("#inbox-capture").fill(text);
  const created = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().includes("/api/inbox/v1/inbox-items"),
  );
  await page.getByRole("button", { name: "Capture" }).click();
  const response = await created;
  expect(response.status()).toBeLessThan(300);
  const id = ((await response.json()) as InboxItem).id;
  return item(request, id);
}

/** Waits for an Inbox item to settle and explains a different outcome. */
async function waitForState(
  request: APIRequestContext,
  id: string,
  state: string,
  timeout = 180_000,
) {
  const settled = new Set([state, "resolved", "human_review", "failed"]);
  await expect
    .poll(async () => settled.has((await item(request, id)).state), {
      timeout,
    })
    .toBe(true);
  const final = (await (
    await request.get(`/api/inbox/v1/inbox-items/${id}`)
  ).json()) as InboxItem & { processingMetadata?: unknown };
  expect(
    final.state,
    `Inbox item ${id} "${final.content}" ended as ${final.state}: ${JSON.stringify(final.processingMetadata)}`,
  ).toBe(state);
}

async function subjectHistory(request: APIRequestContext, taskId: string) {
  const response = await request.get(
    `/api/activity/v1/activity-events?subjectType=task&subjectId=${taskId}&limit=100`,
  );
  return ((await response.json()).items as ActivityEvent[]).reverse();
}

const processorActor = { kind: "agent", name: "codex-inbox-processor" };

test.describe("Phase 8 acceptance", () => {
  test.skip(!enabled, "run with make phase8-acceptance");
  test.describe.configure({ mode: "serial" });
  test.setTimeout(600_000);

  const plumber = `P8 accept call the plumber ${stamp}`;
  const electrician = `P8 accept call the electrician ${stamp}`;
  let undated: Task;
  let undatedCapture: InboxItem;

  test("1. an undated Task created from Inbox stays visible", async ({
    page,
    request,
  }) => {
    undatedCapture = await captureOnHome(
      page,
      request,
      `Create a task titled ${plumber}.`,
    );
    await waitForState(request, undatedCapture.id, "resolved");
    const [task] = await tasksTitled(request, plumber);
    expect(task).toBeTruthy();
    expect(task.dueAt ?? null).toBeNull();
    undated = task;
    await page.goto("/tasks");
    await expect(
      page.getByRole("region", { name: "Unscheduled / No date" }),
    ).toContainText(plumber);
  });

  test("2. the browser edits an existing Task", async ({ page, request }) => {
    await page.goto("/tasks");
    await page.getByLabel(`Edit ${undated.title}`).click();
    await page.getByLabel("Task title", { exact: true }).fill(electrician);
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText(electrician)).toBeVisible();
    await page.reload();
    await expect(page.getByText(electrician)).toBeVisible();
    expect((await getTask(request, undated.id)).title).toBe(electrician);
  });

  test("3. Inbox text reschedules the existing Task without a duplicate", async ({
    page,
    request,
  }) => {
    const capture = await captureOnHome(
      page,
      request,
      `Reschedule my existing task "${electrician}" to 2026-10-20.`,
    );
    await waitForState(request, capture.id, "resolved");
    const task = await getTask(request, undated.id);
    expect(task.dueAt).toBeTruthy();
    const due = Date.parse(task.dueAt!);
    expect(due).toBeGreaterThanOrEqual(Date.parse("2026-10-19T12:00:00Z"));
    expect(due).toBeLessThanOrEqual(Date.parse("2026-10-21T12:00:00Z"));
    expect(await tasksTitled(request, electrician)).toHaveLength(1);
    expect(await tasksTitled(request, `P8 accept call the`)).toHaveLength(1);
  });

  test("4. a voice note completes the existing matching Task", async ({
    page,
    request,
  }) => {
    const chicken = await createTask(request, "Buy chicken for dinner");
    const bytes = Array.from(
      readFileSync(process.env.LIFE_OS_E2E_PHASE8_VOICE!),
    );
    await page.addInitScript((audioBytes: number[]) => {
      const stream = { getTracks: () => [{ stop: () => undefined }] };
      class FixtureMediaRecorder {
        mimeType = "audio/wav";
        ondataavailable?: (event: { data: Blob }) => void;
        onstop?: () => void;
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
    const job = (await (await upload).json()) as { id: string };
    let inboxItemId = "";
    await expect
      .poll(
        async () => {
          const data = await (
            await request.get(`/api/voice/v1/audio-jobs/${job.id}`)
          ).json();
          inboxItemId = data.inboxItemId ?? "";
          return data.status;
        },
        { timeout: 180_000 },
      )
      .toBe("succeeded");
    await expect(page.getByText("Transcribed and added to Inbox.")).toBeVisible(
      { timeout: 15_000 },
    );
    await waitForState(request, inboxItemId, "resolved");
    expect((await getTask(request, chicken.id)).completed).toBe(true);
    expect(await tasksTitled(request, "Buy chicken")).toHaveLength(1);
  });

  test("5-6. an ambiguous mutation waits in Human Review; Home shows it", async ({
    page,
    request,
  }) => {
    const report = await createTask(request, "Call Anna about the report");
    const invoice = await createTask(request, "Call Anna about the invoice");
    const capture = await captureOnHome(
      page,
      request,
      "I already called Anna, mark that task as done.",
    );
    await waitForState(request, capture.id, "human_review");
    for (const task of [report, invoice]) {
      expect((await getTask(request, task.id)).completed).toBe(false);
    }

    // 6. Home is attention-first: the decision is surfaced, with a link.
    await page.goto("/");
    const review = page.locator("#attention-review");
    await expect(review).toContainText("1 capture waiting for your decision");
    await expect(
      page.getByRole("link", { name: "Review in Inbox" }),
    ).toBeVisible();
    for (const card of [
      "Human Review",
      "Urgent tasks",
      "Next on your calendar",
      "Unscheduled tasks",
    ]) {
      await expect(page.getByRole("region", { name: card })).toBeVisible();
    }

    // Still nothing changed until the user decides.
    for (const task of [report, invoice]) {
      expect((await getTask(request, task.id)).completed).toBe(false);
    }
    await page.goto("/inbox");
    await page
      .getByRole("button", { name: "Call Anna about the invoice" })
      .click();
    await expect
      .poll(async () => (await getTask(request, invoice.id)).completed, {
        timeout: 180_000,
      })
      .toBe(true);
    expect((await getTask(request, report.id)).completed).toBe(false);
    await waitForState(request, capture.id, "resolved");
  });

  test("7. the Event Log traces the capture to the Task and its later changes", async ({
    page,
  }) => {
    await page.goto("/activity");
    await page
      .getByRole("combobox", { name: "Event", exact: true })
      .selectOption("inbox.item.captured");
    await page.getByRole("button", { name: "Apply" }).click();
    await page
      .getByRole("list", { name: "Activity entries" })
      .getByRole("link")
      .filter({ hasText: plumber })
      .click();
    const selected = page.getByRole("region", { name: "Selected event" });
    const downstream = selected
      .locator("section")
      .filter({ has: page.getByRole("heading", { name: "What it led to" }) });
    for (const step of [
      "Processing started",
      "Agent action requested: task_create",
      "Task created",
      "Inbox item resolved",
    ]) {
      await expect(downstream.getByText(step, { exact: true })).toBeVisible();
    }
    await downstream.getByRole("link", { name: /^Task created/ }).click();
    const related = selected.locator("section").filter({
      has: page.getByRole("heading", { name: "Same task in other workflows" }),
    });
    await expect(related.getByText(/by You/)).toBeVisible();
    await expect(
      related.getByText(/by Inbox processor \(Codex\)/),
    ).toBeVisible();
  });

  test("8. a provider usage limit schedules a durable retry shown on Status", async ({
    page,
    request,
  }) => {
    const reset = Math.floor(Date.now() / 1000) + 150;
    simulate("limit", reset);
    const title = `P8 accept retry ${stamp}`;
    const capture = await captureOnHome(
      page,
      request,
      `Create a task titled ${title}.`,
    );
    await expect
      .poll(async () => (await item(request, capture.id)).retryReason, {
        timeout: 120_000,
      })
      .toBe("usage_limited");
    const scheduled = await item(request, capture.id);
    const next = Date.parse(scheduled.nextRetryAt!) / 1000;
    expect(next).toBeGreaterThanOrEqual(reset);
    expect(next).toBeLessThanOrEqual(reset + 60);

    await page.goto("/status");
    await expect(
      page.getByRole("region", { name: "Automatic processing" }),
    ).toContainText("Codex has reached its usage limit.");
    await expect(
      page.getByRole("region", { name: "Inbox backlog" }),
    ).toContainText("after the Codex usage limit resets");
    await page.goto("/");
    await expect(
      page.getByRole("link", { name: /^System status:/ }),
    ).toBeVisible();

    simulate("limit", 0);
    await waitForState(request, capture.id, "resolved", 330_000);
    expect(await tasksTitled(request, title)).toHaveLength(1);
  });

  test("9. a processor crash during a run does not duplicate side effects", async ({
    page,
    request,
  }) => {
    const title = `P8 accept crash ${stamp}`;
    const capture = await captureOnHome(
      page,
      request,
      `Create a task titled ${title}.`,
    );
    await expect
      .poll(
        async () => {
          const attempts = await (
            await request.get(
              `/api/inbox/v1/inbox-items/${capture.id}/processing-attempts`,
            )
          ).json();
          return attempts.items?.[0]?.runtimeSessionId ?? "";
        },
        { timeout: 60_000, intervals: [200] },
      )
      .not.toBe("");
    compose("kill", "-s", "KILL", "processor");
    // The run was cut off: the capture is left mid-processing.
    expect((await item(request, capture.id)).state).toBe("processing");
    compose("start", "processor");
    await waitForState(request, capture.id, "resolved", 330_000);
    expect(await tasksTitled(request, title)).toHaveLength(1);
    const attempts = await (
      await request.get(
        `/api/inbox/v1/inbox-items/${capture.id}/processing-attempts`,
      )
    ).json();
    expect(
      attempts.items.some((attempt: { errorSummary?: string }) =>
        attempt.errorSummary?.startsWith("recovered"),
      ),
    ).toBe(true);
  });

  test("10. UI and agent mutations go through the same owning-service operations", async ({
    page,
    request,
  }) => {
    // The plumber/electrician Task was edited by the user (chain 2) and by the
    // agent (chain 3): both are the Tasks service's own task.updated.
    const history = await subjectHistory(request, undated.id);
    const updates = history.filter((event) => event.type === "task.updated");
    expect(updates.map((event) => event.source)).toEqual(["tasks", "tasks"]);
    expect(updates[0].actor).toEqual({ kind: "user" });
    expect(updates[1].actor).toEqual(processorActor);
    expect(updates[0].correlationId).not.toBe(updates[1].correlationId);

    // Completion: by the user in the Tasks workspace, and by the agent from
    // a voice note (chain 4); both are task.completed from Tasks.
    const report = (
      await tasksTitled(request, "Call Anna about the report")
    )[0];
    await page.goto("/tasks");
    await page.getByLabel(`Complete ${report.title}`).click();
    await expect
      .poll(async () => (await getTask(request, report.id)).completed)
      .toBe(true);
    const byUser = (await subjectHistory(request, report.id)).find(
      (event) => event.type === "task.completed",
    );
    const chicken = (await tasksTitled(request, "Buy chicken"))[0];
    const byAgent = (await subjectHistory(request, chicken.id)).find(
      (event) => event.type === "task.completed",
    );
    expect(byUser).toMatchObject({ source: "tasks", actor: { kind: "user" } });
    expect(byAgent).toMatchObject({ source: "tasks", actor: processorActor });
  });
});
