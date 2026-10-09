# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: csv-import.spec.ts >> Dashboard CSV import >> uploads through the browser file chooser and reports persisted import counts
- Location: e2e/csv-import.spec.ts:9:7

# Error details

```
Test timeout of 180000ms exceeded.
```

```
Error: locator.setInputFiles: Test timeout of 180000ms exceeded.
Call log:
  - waiting for locator('input[type="file"]')

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
                - generic [ref=e55]: 1 capture needs a decision
              - generic [ref=e56]:
                - paragraph [ref=e57]: Complete P8 review 1791555207804
                - link "Open workspace" [ref=e58] [cursor=pointer]:
                  - /url: /inbox
            - generic [ref=e61]:
              - generic [ref=e62]:
                - generic [ref=e63]: Urgent tasks
                - generic [ref=e68]: 8 overdue · 0 due today
              - generic [ref=e69]:
                - paragraph [ref=e70]: P8 home urgent 1791555207790-7
                - paragraph [ref=e71]: P8 home urgent 1791555207790-6
                - paragraph [ref=e72]: P8 home urgent 1791555207790-5
                - paragraph [ref=e73]: P8 home urgent 1791555207790-4
                - paragraph [ref=e74]: P8 home urgent 1791555207790-3
                - link "Open workspace" [ref=e75] [cursor=pointer]:
                  - /url: /tasks
            - generic [ref=e78]:
              - generic [ref=e79]:
                - generic [ref=e80]: Next on your calendar
                - generic [ref=e84]: Calendar is unavailable
              - link "Open workspace" [ref=e86] [cursor=pointer]:
                - /url: /calendar
            - generic [ref=e89]:
              - generic [ref=e90]:
                - generic [ref=e91]: Unscheduled tasks
                - generic [ref=e96]: A few open tasks without a date
              - generic [ref=e97]:
                - paragraph [ref=e98]: P8 review candidate 1791555207804
                - link "Open workspace" [ref=e99] [cursor=pointer]:
                  - /url: /tasks
  - alert [ref=e102]
```

# Test source

```ts
  1  | import { expect, test } from "@playwright/test";
  2  | 
  3  | test.describe("Dashboard CSV import", () => {
  4  |   test.skip(
  5  |     !process.env.LIFE_OS_E2E_URL || !process.env.LIFE_OS_E2E_ACCOUNT_ID,
  6  |     "requires isolated gateway and seeded account",
  7  |   );
  8  | 
  9  |   test("uploads through the browser file chooser and reports persisted import counts", async ({
  10 |     page,
  11 |     request,
  12 |   }) => {
  13 |     const externalId = `browser-csv-${Date.now()}`;
  14 |     const csv = [
  15 |       "account_id,occurred_on,amount_minor,currency,external_id,note",
  16 |       `${process.env.LIFE_OS_E2E_ACCOUNT_ID},2026-10-08,-125,USD,${externalId},Browser CSV`,
  17 |     ].join("\n");
  18 |     await page.goto("/");
> 19 |     await page.locator('input[type="file"]').setInputFiles({
     |     ^ Error: locator.setInputFiles: Test timeout of 180000ms exceeded.
  20 |       name: "browser-import.csv",
  21 |       mimeType: "text/csv",
  22 |       buffer: Buffer.from(csv),
  23 |     });
  24 |     await expect(page.getByRole("status")).toContainText(
  25 |       "Imported 1; skipped 0; failed 0.",
  26 |     );
  27 |     const response = await request.get(
  28 |       `/api/finance/v1/transactions?limit=100`,
  29 |     );
  30 |     expect(response.ok()).toBeTruthy();
  31 |     const data = await response.json();
  32 |     expect(
  33 |       data.items.some((item: { note: string }) => item.note === "Browser CSV"),
  34 |     ).toBeTruthy();
  35 |   });
  36 | });
  37 | 
```