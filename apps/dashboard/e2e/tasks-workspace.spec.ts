import { expect, test } from "@playwright/test";

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
    await page.getByRole("button", { name: "Schedule" }).click();
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
});
