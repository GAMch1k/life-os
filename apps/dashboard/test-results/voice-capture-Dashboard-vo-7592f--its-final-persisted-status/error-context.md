# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: voice-capture.spec.ts >> Dashboard voice capture >> records in the browser, uploads through the Voice UI, and renders its final persisted status
- Location: e2e/voice-capture.spec.ts:16:7

# Error details

```
Test timeout of 240000ms exceeded.
```

```
Error: locator.click: Test timeout of 240000ms exceeded.
Call log:
  - waiting for getByRole('button', { name: 'Record' })

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
  1   | import { expect, test } from "@playwright/test";
  2   | import { readFile } from "node:fs/promises";
  3   | 
  4   | const enabled = Boolean(process.env.LIFE_OS_E2E_URL);
  5   | const jfkFixture =
  6   |   "https://github.com/ggerganov/whisper.cpp/raw/master/samples/jfk.wav";
  7   | const localFixture = process.env.LIFE_OS_E2E_VOICE_AUDIO_FILE;
  8   | const expectedTaskTitle = process.env.LIFE_OS_E2E_EXPECT_VOICE_TASK_TITLE;
  9   | const expectedCompletedTaskTitle =
  10  |   process.env.LIFE_OS_E2E_EXPECT_VOICE_COMPLETED_TASK_TITLE;
  11  | 
  12  | test.describe("Dashboard voice capture", () => {
  13  |   test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");
  14  |   test.setTimeout(240_000);
  15  | 
  16  |   test("records in the browser, uploads through the Voice UI, and renders its final persisted status", async ({
  17  |     page,
  18  |     request,
  19  |   }) => {
  20  |     let completedTaskId: string | undefined;
  21  |     if (expectedCompletedTaskTitle) {
  22  |       const response = await request.post("/api/tasks/v1/tasks", {
  23  |         data: { title: expectedCompletedTaskTitle },
  24  |       });
  25  |       expect(response.status()).toBe(201);
  26  |       completedTaskId = (await response.json()).id;
  27  |     }
  28  |     // This remains a browser workflow: the MediaRecorder fixture stands in for
  29  |     // microphone hardware, but the VoiceCapture component owns the upload and
  30  |     // status polling path. The temporary fixture is deliberately not checked
  31  |     // into the repository.
  32  |     const bytes = localFixture
  33  |       ? Array.from(await readFile(localFixture))
  34  |       : await downloadFixture();
  35  | 
  36  |     await page.addInitScript((audioBytes: number[]) => {
  37  |       type FixtureStream = { getTracks: () => Array<{ stop: () => void }> };
  38  |       const stream: FixtureStream = {
  39  |         getTracks: () => [{ stop: () => undefined }],
  40  |       };
  41  |       class FixtureMediaRecorder {
  42  |         mimeType = "audio/wav";
  43  |         ondataavailable?: (event: { data: Blob }) => void;
  44  |         onstop?: () => void;
  45  | 
  46  |         constructor(readonly stream: FixtureStream) {}
  47  |         start() {}
  48  |         stop() {
  49  |           this.ondataavailable?.({
  50  |             data: new Blob([Uint8Array.from(audioBytes).buffer], {
  51  |               type: this.mimeType,
  52  |             }),
  53  |           });
  54  |           this.onstop?.();
  55  |         }
  56  |       }
  57  | 
  58  |       Object.defineProperty(navigator, "mediaDevices", {
  59  |         configurable: true,
  60  |         value: { getUserMedia: async () => stream },
  61  |       });
  62  |       Object.defineProperty(window, "MediaRecorder", {
  63  |         configurable: true,
  64  |         value: FixtureMediaRecorder,
  65  |       });
  66  |     }, bytes);
  67  | 
  68  |     await page.goto("/");
> 69  |     await page.getByRole("button", { name: "Record" }).click();
      |                                                        ^ Error: locator.click: Test timeout of 240000ms exceeded.
  70  |     const upload = page.waitForResponse(
  71  |       (response) =>
  72  |         response.request().method() === "POST" &&
  73  |         response.url().includes("/api/voice/v1/audio-jobs"),
  74  |     );
  75  |     await page.getByRole("button", { name: "Stop" }).click();
  76  |     const created = await upload;
  77  |     expect(created.status()).toBe(202);
  78  |     const job = (await created.json()) as { id: string };
  79  |     expect(job.id).toBeTruthy();
  80  | 
  81  |     let finalJob: { status: string; inboxItemId?: string } | undefined;
  82  |     await expect
  83  |       .poll(
  84  |         async () => {
  85  |           const status = await request.get(
  86  |             `/api/voice/v1/audio-jobs/${job.id}`,
  87  |           );
  88  |           const data = (await status.json()) as {
  89  |             status: string;
  90  |             inboxItemId?: string;
  91  |           };
  92  |           finalJob = data;
  93  |           return data.status;
  94  |         },
  95  |         { timeout: 180_000 },
  96  |       )
  97  |       .toBe("succeeded");
  98  |     await expect(page.getByText("Transcribed and added to Inbox.")).toBeVisible(
  99  |       {
  100 |         timeout: 15_000,
  101 |       },
  102 |     );
  103 | 
  104 |     if (expectedTaskTitle) {
  105 |       expect(finalJob?.inboxItemId).toBeTruthy();
  106 |       const inboxItemId = finalJob?.inboxItemId;
  107 |       if (!inboxItemId)
  108 |         throw new Error("succeeded Voice job has no Inbox item");
  109 |       await expect
  110 |         .poll(
  111 |           async () => {
  112 |             const attempts = await request.get(
  113 |               `/api/inbox/v1/inbox-items/${inboxItemId}/processing-attempts`,
  114 |             );
  115 |             const data = await attempts.json();
  116 |             return data.items?.[0]?.status;
  117 |           },
  118 |           { timeout: 150_000 },
  119 |         )
  120 |         .toBe("processed");
  121 |       await expect
  122 |         .poll(
  123 |           async () => {
  124 |             const tasks = await request.get(
  125 |               `/api/tasks/v1/tasks?query=${encodeURIComponent(expectedTaskTitle)}`,
  126 |             );
  127 |             const data = await tasks.json();
  128 |             return data.items?.some((entry: { title: string }) =>
  129 |               entry.title
  130 |                 .toLocaleLowerCase()
  131 |                 .includes(expectedTaskTitle.toLocaleLowerCase()),
  132 |             );
  133 |           },
  134 |           { timeout: 30_000 },
  135 |         )
  136 |         .toBeTruthy();
  137 |     }
  138 |     if (expectedCompletedTaskTitle && completedTaskId) {
  139 |       await expect
  140 |         .poll(
  141 |           async () => {
  142 |             const response = await request.get(
  143 |               `/api/tasks/v1/tasks/${completedTaskId}`,
  144 |             );
  145 |             return (await response.json()).completed;
  146 |           },
  147 |           { timeout: 150_000 },
  148 |         )
  149 |         .toBe(true);
  150 |     }
  151 |   });
  152 | });
  153 | 
  154 | async function downloadFixture() {
  155 |   const fixture = await fetch(jfkFixture);
  156 |   expect(fixture.ok).toBeTruthy();
  157 |   return Array.from(new Uint8Array(await fixture.arrayBuffer()));
  158 | }
  159 | 
```