# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: home-attention.spec.ts >> Home attention surface >> keeps large task sets compact and links to their workspaces
- Location: e2e/home-attention.spec.ts:7:7

# Error details

```
Error: expect(locator).toBeVisible() failed

Locator: getByText('P8 home urgent 1791555207790-0')
Expected: visible
Timeout: 5000ms
Error: element(s) not found

Call log:
  - Expect "toBeVisible" getByText('P8 home urgent 1791555207790-0') with timeout 5000ms
  - waiting for getByText('P8 home urgent 1791555207790-0')

```

```yaml
- link "Skip to content":
  - /url: "#main-content"
- banner:
  - paragraph: Life OS
  - paragraph: Personal workspace
  - button "Account controls are not available yet" [disabled]
- complementary:
  - navigation "Main navigation":
    - link "Overview":
      - /url: /
    - link "Finance":
      - /url: /finance
    - link "Tasks":
      - /url: /tasks
    - link "Inbox":
      - /url: /inbox
    - link "Calendar":
      - /url: /calendar
- main:
  - paragraph: Overview
  - heading "What needs your attention?" [level=1]
  - paragraph: A short control surface for decisions and deadlines. Use the dedicated workspaces for the full picture.
  - text: Human Review 1 capture needs a decision
  - paragraph: Complete P8 review 1791555207804
  - link "Open workspace":
    - /url: /inbox
  - text: Urgent tasks 8 overdue · 0 due today
  - paragraph: P8 home urgent 1791555207790-7
  - paragraph: P8 home urgent 1791555207790-6
  - paragraph: P8 home urgent 1791555207790-5
  - paragraph: P8 home urgent 1791555207790-4
  - paragraph: P8 home urgent 1791555207790-3
  - link "Open workspace":
    - /url: /tasks
  - text: Next on your calendar Calendar is unavailable
  - link "Open workspace":
    - /url: /calendar
  - text: Unscheduled tasks A few open tasks without a date
  - paragraph: P8 review candidate 1791555207804
  - link "Open workspace":
    - /url: /tasks
- alert
```

# Test source

```ts
  1  | import { expect, test } from "@playwright/test";
  2  | 
  3  | const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
  4  | 
  5  | test.describe("Home attention surface", () => {
  6  |   test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  7  |   test("keeps large task sets compact and links to their workspaces", async ({
  8  |     page,
  9  |     request,
  10 |   }) => {
  11 |     const stamp = Date.now();
  12 |     const dueAt = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
  13 |     for (let index = 0; index < 8; index++) {
  14 |       const response = await request.post("/api/tasks/v1/tasks", {
  15 |         data: { title: `P8 home urgent ${stamp}-${index}`, dueAt },
  16 |       });
  17 |       expect(response.status()).toBe(201);
  18 |     }
  19 |     await page.goto("/");
  20 |     await expect(
  21 |       page.getByRole("heading", { name: "What needs your attention?" }),
  22 |     ).toBeVisible();
> 23 |     await expect(page.getByText(`P8 home urgent ${stamp}-0`)).toBeVisible();
     |                                                               ^ Error: expect(locator).toBeVisible() failed
  24 |     await expect(page.getByText(`P8 home urgent ${stamp}-5`)).not.toBeVisible();
  25 |     await expect(
  26 |       page.getByRole("link", { name: "Open workspace" }).first(),
  27 |     ).toBeVisible();
  28 |     await expect(page.locator('a[href="/tasks"]')).toHaveCount(2);
  29 |   });
  30 | });
  31 | 
```