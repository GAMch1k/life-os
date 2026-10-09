# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: text-capture.spec.ts >> Dashboard capture → automatic processor >> persists a browser capture and automatically creates a Task
- Location: e2e/text-capture.spec.ts:8:7

# Error details

```
Test timeout of 180000ms exceeded.
```

```
Error: locator.fill: Test timeout of 180000ms exceeded.
Call log:
  - waiting for locator('#inbox-capture')

```

# Page snapshot

```yaml
- generic [active] [ref=e1]:
  - generic [ref=e2]:
    - link "Skip to content" [ref=e3] [cursor=pointer]:
      - /url: "#main-content"
    - banner [ref=e4]:
      - generic [ref=e5]:
        - generic [ref=e11]:
          - paragraph [ref=e12]: Life OS
          - paragraph [ref=e13]: Personal workspace
        - button "Account controls are not available yet" [disabled]
    - generic [ref=e14]:
      - complementary [ref=e15]:
        - navigation "Main navigation" [ref=e16]:
          - link "Overview" [ref=e17] [cursor=pointer]:
            - /url: /
          - link "Finance" [ref=e24] [cursor=pointer]:
            - /url: /finance
          - link "Tasks" [ref=e28] [cursor=pointer]:
            - /url: /tasks
          - link "Inbox" [ref=e33] [cursor=pointer]:
            - /url: /inbox
          - link "Calendar" [ref=e38] [cursor=pointer]:
            - /url: /calendar
      - main [ref=e42]:
        - generic [ref=e43]:
          - generic [ref=e44]:
            - paragraph [ref=e45]: Overview
            - heading "What needs your attention?" [level=1] [ref=e46]
            - paragraph [ref=e47]: A short control surface for decisions and deadlines. Use the dedicated workspaces for the full picture.
          - generic [ref=e48]:
            - generic [ref=e49]:
              - generic [ref=e50]:
                - generic [ref=e51]: Human Review
                - generic [ref=e55]: Nothing waiting for review
              - link "Open workspace" [ref=e57] [cursor=pointer]:
                - /url: /inbox
            - generic [ref=e60]:
              - generic [ref=e61]:
                - generic [ref=e62]: Urgent tasks
                - generic [ref=e67]: 9 overdue · 1 due today
              - generic [ref=e68]:
                - paragraph [ref=e69]: P8 overdue 1791555208778
                - paragraph [ref=e70]: P8 home urgent 1791555207790-7
                - paragraph [ref=e71]: P8 home urgent 1791555207790-6
                - paragraph [ref=e72]: P8 home urgent 1791555207790-5
                - paragraph [ref=e73]: P8 home urgent 1791555207790-4
                - link "Open workspace" [ref=e74] [cursor=pointer]:
                  - /url: /tasks
            - generic [ref=e77]:
              - generic [ref=e78]:
                - generic [ref=e79]: Next on your calendar
                - generic [ref=e83]: Calendar is unavailable
              - link "Open workspace" [ref=e85] [cursor=pointer]:
                - /url: /calendar
            - generic [ref=e88]:
              - generic [ref=e89]:
                - generic [ref=e90]: Unscheduled tasks
                - generic [ref=e95]: A few open tasks without a date
              - generic [ref=e96]:
                - paragraph [ref=e97]: P8 renamed 1791555209608
                - paragraph [ref=e98]: P8 unscheduled 1791555208778
                - link "Open workspace" [ref=e99] [cursor=pointer]:
                  - /url: /tasks
  - alert [ref=e102]
```

# Test source

```ts
  1  | import { expect, test } from "@playwright/test";
  2  | 
  3  | const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
  4  | 
  5  | test.describe("Dashboard capture → automatic processor", () => {
  6  |   test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  7  | 
  8  |   test("persists a browser capture and automatically creates a Task", async ({
  9  |     page,
  10 |     request,
  11 |   }) => {
  12 |     const title = `P7 browser capture ${Date.now()}`;
  13 |     await page.goto("/");
> 14 |     await page.locator("#inbox-capture").fill(`Create a task titled ${title}.`);
     |                                          ^ Error: locator.fill: Test timeout of 180000ms exceeded.
  15 |     await page.getByRole("button", { name: "Capture" }).click();
  16 |     await expect(page.getByText(title)).toBeVisible();
  17 | 
  18 |     const inbox = await request.get(
  19 |       "/api/inbox/v1/inbox-items?query=state=raw&limit=100",
  20 |     );
  21 |     expect(inbox.ok()).toBeTruthy();
  22 |     const item = (await inbox.json()).items.find((entry: { content: string }) =>
  23 |       entry.content.includes(title),
  24 |     );
  25 |     expect(item).toBeTruthy();
  26 | 
  27 |     await expect
  28 |       .poll(
  29 |         async () => {
  30 |           const attempts = await request.get(
  31 |             `/api/inbox/v1/inbox-items/${item.id}/processing-attempts`,
  32 |           );
  33 |           const data = await attempts.json();
  34 |           return data.items?.[0]?.status;
  35 |         },
  36 |         { timeout: 150_000 },
  37 |       )
  38 |       .toBe("processed");
  39 | 
  40 |     await expect
  41 |       .poll(
  42 |         async () => {
  43 |           const tasks = await request.get(
  44 |             `/api/tasks/v1/tasks?query=${encodeURIComponent(title)}`,
  45 |           );
  46 |           const data = await tasks.json();
  47 |           return data.items?.some((entry: { title: string }) =>
  48 |             entry.title.startsWith(title),
  49 |           );
  50 |         },
  51 |         { timeout: 30_000 },
  52 |       )
  53 |       .toBeTruthy();
  54 |   });
  55 | });
  56 | 
```