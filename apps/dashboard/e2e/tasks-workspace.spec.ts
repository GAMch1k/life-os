import { expect, test } from "./fixtures";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
const iso = (date: Date) => date.toISOString();
const dayKey = (date: Date) => date.toLocaleDateString("en-CA");

test.describe("Tasks workspace", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("groups open Tasks and projects scheduled work by focused date", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const today = new Date();
    today.setHours(12, 0, 0, 0);
    const overdue = new Date(today);
    overdue.setDate(overdue.getDate() - 2);
    const upcoming = new Date(today);
    upcoming.setDate(upcoming.getDate() + 2);
    for (const [title, dueAt] of [
      [`P8 overdue ${stamp}`, iso(overdue)],
      [`P8 today ${stamp}`, iso(today)],
      [`P8 upcoming ${stamp}`, iso(upcoming)],
      [`P8 unscheduled ${stamp}`, undefined],
    ] as const) {
      const response = await request.post("/api/tasks/v1/tasks", {
        data: { title, ...(dueAt ? { dueAt } : {}) },
      });
      expect(response.status()).toBe(201);
    }
    await page.goto("/tasks");
    for (const title of ["overdue", "today", "upcoming", "unscheduled"])
      await expect(page.getByText(`P8 ${title} ${stamp}`)).toBeVisible();
    await page.getByRole("button", { name: "Schedule", exact: true }).click();
    await expect(
      page.getByTestId(`schedule-day-${dayKey(today)}`),
    ).toContainText(`P8 today ${stamp}`);
    await page.getByTestId(`task-day-${dayKey(upcoming)}`).click();
    await expect(
      page.getByTestId(`schedule-day-${dayKey(upcoming)}`),
    ).toContainText(`P8 upcoming ${stamp}`);
    await page.reload();
    await page.getByRole("button", { name: "List" }).click();
    await expect(page.getByText(`P8 unscheduled ${stamp}`)).toBeVisible();
  });

  test("edits and reopens an existing Task through the owning service", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const original = `P8 lifecycle ${stamp}`;
    const renamed = `P8 renamed ${stamp}`;
    const notes = `notes ${stamp}`;
    const created = await request.post("/api/tasks/v1/tasks", {
      data: { title: original },
    });
    expect(created.status()).toBe(201);
    const task = await created.json();

    await page.goto("/tasks");
    await page.getByLabel(`Edit ${original}`).click();
    await page.getByLabel("Task title").fill(renamed);
    await page.getByLabel("Task notes").fill(notes);
    await page.getByLabel("Due date").fill("2026-10-21T09:30");
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByText(renamed)).toBeVisible();

    await page.getByLabel(`Edit ${renamed}`).click();
    await page.getByRole("button", { name: "Clear date" }).click();
    await expect(
      page.getByRole("region", { name: "Unscheduled / No date" }),
    ).toContainText(renamed);

    await page.getByLabel(`Complete ${renamed}`).click();
    await expect(page.getByRole("region", { name: "Completed" })).toContainText(
      renamed,
    );
    await page.getByLabel(`Reopen ${renamed}`).click();
    await expect(
      page.getByRole("region", { name: "Unscheduled / No date" }),
    ).toContainText(renamed);

    await page.reload();
    const persisted = await request.get(`/api/tasks/v1/tasks/${task.id}`);
    expect(persisted.ok()).toBeTruthy();
    const persistedTask = await persisted.json();
    expect(persistedTask).toMatchObject({
      title: renamed,
      notes,
      completed: false,
    });
    expect(persistedTask.dueAt).toBeFalsy();
  });
});
