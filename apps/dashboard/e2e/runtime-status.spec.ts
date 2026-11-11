import { execFileSync } from "node:child_process";
import path from "node:path";

import {
  createFeed,
  deleteFeed,
  failingFeedUrl,
  FEED_OVERLAY_HINT,
  feedDir,
  refreshFeed,
  waitForRefresh,
  writeFeed,
} from "./calendar-feed";
import { expect, test, type Page } from "./fixtures";

// Forces real dependency and worker problems on the disposable verification
// stack and checks that the Dashboard explains each one. Faults are injected
// only through tools/verify-compose, which refuses non-verification resources.
// Run with `make runtime-status-smoke`; it stops services, so it never runs as
// part of the parallel browser suite. Calendar feed health and the card
// layout (at the end) run in the browser suite too: a feed failing once raises
// no Home alert, and the cards pack into two level columns (POL-10).
const enabled = Boolean(
  process.env.LIFE_OS_E2E_URL && process.env.LIFE_OS_E2E_FAULTS,
);
const repoRoot = path.resolve(process.cwd(), "../..");

function compose(...args: string[]) {
  execFileSync("./tools/verify-compose", args, {
    cwd: repoRoot,
    stdio: "ignore",
  });
}

/** Starts host-health again reporting a made-up host (its fixture). */
function hostHealthFixture(fixture: "healthy" | "low-memory") {
  execFileSync(
    "./tools/verify-compose",
    ["up", "--detach", "--no-build", "--no-deps", "host-health"],
    {
      cwd: repoRoot,
      stdio: "ignore",
      env: { ...process.env, LIFE_OS_VERIFY_HOST_HEALTH_FIXTURE: fixture },
    },
  );
}

async function expectCheck(
  page: Page,
  check: string,
  text: string | RegExp,
  timeout = 120_000,
) {
  await expect(async () => {
    await page.goto("/status");
    await expect(page.getByRole("region", { name: check })).toContainText(
      text,
      { timeout: 1_000 },
    );
  }).toPass({ timeout, intervals: [2_000] });
}

async function expectHomeAlert(page: Page, text: RegExp) {
  await page.goto("/");
  const alert = page.getByRole("link", { name: /^System status:/ });
  await expect(alert).toContainText(text);
  await alert.click();
  await expect(
    page.getByRole("heading", { level: 1, name: "Status", exact: true }),
  ).toBeVisible();
}

/** Half a second of 16 kHz mono silence. */
function silentWav() {
  const samples = 8_000;
  const buffer = Buffer.alloc(44 + samples * 2);
  buffer.write("RIFF", 0);
  buffer.writeUInt32LE(36 + samples * 2, 4);
  buffer.write("WAVEfmt ", 8);
  buffer.writeUInt32LE(16, 16);
  buffer.writeUInt16LE(1, 20);
  buffer.writeUInt16LE(1, 22);
  buffer.writeUInt32LE(16_000, 24);
  buffer.writeUInt32LE(32_000, 28);
  buffer.writeUInt16LE(2, 32);
  buffer.writeUInt16LE(16, 34);
  buffer.write("data", 36);
  buffer.writeUInt32LE(samples * 2, 40);
  return buffer;
}

test.describe("Runtime status under forced problems", () => {
  test.skip(
    !enabled,
    "set LIFE_OS_E2E_URL and LIFE_OS_E2E_FAULTS (make runtime-status-smoke)",
  );
  test.describe.configure({ mode: "serial" });
  test.setTimeout(420_000);

  test("a healthy stack reports everything working and Home stays quiet", async ({
    page,
  }) => {
    await expect(async () => {
      await page.goto("/status");
      await expect(page.getByRole("status")).toHaveText(
        "Everything is working.",
        { timeout: 1_000 },
      );
    }).toPass({ timeout: 120_000, intervals: [2_000] });
    // Account usage is read from the Codex CLI inside the processor
    // container (P8-10).
    await expectCheck(
      page,
      "Automatic processing",
      /Codex 5-hour usage\d+% used, resets/,
    );
    await expect(
      page.getByRole("region", { name: "Automatic processing" }),
    ).toContainText("Codex plan");
    await page.goto("/");
    await expect(
      page.getByRole("link", { name: /^System status:/ }),
    ).toHaveCount(0);
  });

  test("a stopped Whisper is reported on Status and Home", async ({ page }) => {
    compose("stop", "whisper");
    try {
      await expectCheck(
        page,
        "Voice transcription",
        "Whisper speech-to-text is not ready.",
      );
      await expect(
        page.getByRole("region", { name: "Voice transcription" }),
      ).toContainText("Recordings are kept");
      await expectHomeAlert(page, /Whisper speech-to-text is not ready/);
    } finally {
      compose("start", "whisper");
    }
    await expectCheck(page, "Voice transcription", "Idle.", 240_000);
  });

  test("a stopped processor is reported", async ({ page }) => {
    compose("stop", "processor");
    try {
      await expectCheck(
        page,
        "Automatic processing",
        "The Inbox processor is not running.",
      );
      await expectHomeAlert(page, /Inbox processor is not running/);
    } finally {
      compose("start", "processor");
    }
    await expectCheck(
      page,
      "Automatic processing",
      /Waiting for work|Processing normally/,
    );
  });

  test("an MCP outage shows the processor's own error", async ({ page }) => {
    compose("stop", "mcp");
    try {
      await expectCheck(
        page,
        "Automatic processing",
        "The processor cannot read Inbox through MCP.",
      );
    } finally {
      compose("start", "mcp");
    }
    await expectCheck(
      page,
      "Automatic processing",
      /Waiting for work|Processing normally/,
    );
  });

  test("a signed-out Codex is reported with the fix, then recovers", async ({
    page,
  }) => {
    // Remove only the container's tmpfs copy of the credentials; the host
    // session is mounted read-only and untouched.
    compose(
      "exec",
      "-T",
      "processor",
      "sh",
      "-c",
      'rm -f "$CODEX_HOME/auth.json"',
    );
    const title = `P8 status signed-out ${Date.now()}`;
    try {
      await page.goto("/");
      await page
        .locator("#inbox-capture")
        .fill(`Create a task titled ${title}.`);
      await page.getByRole("button", { name: "Capture" }).click();
      await expectCheck(
        page,
        "Automatic processing",
        "Codex is not signed in.",
        240_000,
      );
      const processing = page.getByRole("region", {
        name: "Automatic processing",
      });
      await expect(processing).toContainText("codex login");
      await expect(processing).toContainText("401 Unauthorized");
      await expectCheck(page, "Inbox backlog", /failed and is being retried/);
      await expectHomeAlert(page, /Codex is not signed in/);
    } finally {
      // "Sign in again" by restoring the copy from the read-only mount. A
      // restart would also work, but it kills the in-flight attempt, which
      // leaves the capture in processing (tracked for P8-11/P8-12).
      compose(
        "exec",
        "-T",
        "processor",
        "sh",
        "-c",
        'cp /run/lifeos-codex-source/auth.json "$CODEX_HOME/auth.json"',
      );
    }
    await expectCheck(
      page,
      "Automatic processing",
      "Processing normally.",
      240_000,
    );
    await expectCheck(page, "Inbox backlog", "Nothing waiting.");
  });

  test("a stopped Tasks service is named", async ({ page }) => {
    compose("stop", "tasks");
    try {
      await expectCheck(page, "Services", "Tasks is not ready.");
      // Its reminder health cannot be read either; Services already names
      // Tasks, so Notifications leaves Task reminders out.
      await expect(
        page.getByRole("region", { name: "Notifications" }),
      ).not.toContainText("Task reminders");
      await expectHomeAlert(page, /Tasks is not ready/);
    } finally {
      compose("start", "tasks");
    }
    await expectCheck(page, "Services", "All services are ready.");
  });

  test("low memory on the server is a problem naming the biggest users", async ({
    page,
  }) => {
    hostHealthFixture("low-memory");
    try {
      await expectCheck(
        page,
        "Server",
        /Memory is very low: 520 MiB available of 7\.7 GiB\. Biggest: lifeos-whisper-1 1\.9 GiB, homeassistant 600 MiB/,
      );
      await expect(page.getByRole("region", { name: "Server" })).toContainText(
        "docker stats shows which containers use the most",
      );
      await expectHomeAlert(page, /Memory is very low/);
    } finally {
      hostHealthFixture("healthy");
    }
    await expectCheck(page, "Server", /Running normally/);
  });

  test("a stopped host-health is one alert, under Services", async ({
    page,
  }) => {
    compose("stop", "host-health");
    try {
      await expectCheck(page, "Services", "Host health is not ready.");
      await expect(page.getByRole("region", { name: "Server" })).toHaveCount(0);
    } finally {
      compose("start", "host-health");
    }
    await expectCheck(page, "Server", /Running normally/);
  });

  test("a stopped backup service is one alert, under Services", async ({
    page,
  }) => {
    compose("stop", "backup");
    try {
      await expectCheck(page, "Services", "Backup is not ready.");
      // Its status cannot be read either; Services already names it, so the
      // Backups card is left out rather than alerting twice.
      await expect(page.getByRole("region", { name: "Backups" })).toHaveCount(
        0,
      );
    } finally {
      compose("start", "backup");
    }
    await expectCheck(page, "Services", "All services are ready.");
    await expect(page.getByRole("region", { name: "Backups" })).toBeVisible();
  });

  test("a Voice worker that stops picking up recordings is reported", async ({
    page,
    request,
  }) => {
    compose("stop", "voice-worker");
    try {
      const upload = await request.post("/api/voice/v1/audio-jobs", {
        multipart: {
          audio: {
            name: "silence.wav",
            mimeType: "audio/wav",
            buffer: silentWav(),
          },
          source: "web",
        },
      });
      expect(upload.status()).toBe(202);
      // Due work is stalled once it has waited two minutes with nothing
      // processing.
      await expectCheck(
        page,
        "Voice transcription",
        "The Voice worker is not picking up recordings.",
        240_000,
      );
      await expectHomeAlert(page, /Voice worker is not picking up/);
    } finally {
      compose("start", "voice-worker");
    }
  });

  test("a calendar feed that keeps failing reaches Home only after repeated failures", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const name = `E2E broken feed ${stamp}`;
    const source = await createFeed(request, name, failingFeedUrl(stamp));
    try {
      await waitForRefresh(request, source.id, "failed");
      await expectCheck(page, "Calendar sources", name);
      await page.goto("/");
      await expect(
        page.getByRole("link", { name: new RegExp(`System status:.*${name}`) }),
      ).toHaveCount(0);

      // "Refresh now" until it has failed three times in a row.
      let failures = 1;
      for (let attempt = 0; failures < 3 && attempt < 6; attempt++) {
        failures = (await refreshFeed(request, source.id)).refresh
          .consecutiveFailures;
      }
      expect(failures).toBeGreaterThanOrEqual(3);
      await expectCheck(
        page,
        "Calendar sources",
        new RegExp(
          `The calendar feed ${name} has failed \\d+ refreshes in a row`,
        ),
      );
      await expectHomeAlert(page, new RegExp(`${name} has failed`));
    } finally {
      await deleteFeed(request, source.id);
    }
    await expectCheck(
      page,
      "Calendar sources",
      /No calendar feeds|refreshing normally/,
    );
  });
});

test.describe("Calendar feed health on Status", () => {
  test.skip(
    !process.env.LIFE_OS_E2E_URL,
    "set LIFE_OS_E2E_URL to run against the isolated stack",
  );

  test("a failed refresh shows the feed's error and next try, without a Home alert", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const name = `E2E status feed ${stamp}`;
    const source = await createFeed(request, name, failingFeedUrl(stamp));
    try {
      const failed = await waitForRefresh(request, source.id, "failed");
      await page.goto("/status");
      const feed = page
        .getByRole("region", { name: "Calendar sources" })
        .getByRole("listitem")
        .filter({ hasText: name });
      await expect(feed).toContainText(
        "The last refresh failed; it is retried automatically.",
      );
      await expect(feed).toContainText(failed.refresh.lastError!);
      for (const fact of ["Last refresh", "Last success", "Next refresh"]) {
        await expect(feed).toContainText(fact);
      }
      await expect(feed).toContainText(/Last refreshfailed \(/);
      await expect(feed).toContainText("Last successnever");
      // The feed URL is never shown.
      await expect(page.getByText(`e2e-${stamp}.ics`)).toHaveCount(0);

      // calendar-feeds is one of the services Status (and the monitor)
      // watches.
      await expect(
        page.getByRole("region", { name: "Services" }),
      ).toContainText("Calendar feedsready");

      // One failure is retried automatically: Home stays quiet about it.
      await page.goto("/");
      await expect(
        page.getByRole("link", { name: new RegExp(`System status:.*${name}`) }),
      ).toHaveCount(0);
    } finally {
      await deleteFeed(request, source.id);
    }
  });

  test("an imported feed shows what its last refresh did", async ({
    page,
    request,
  }) => {
    test.skip(!feedDir, FEED_OVERLAY_HINT);
    const stamp = Date.now();
    const name = `E2E healthy feed ${stamp}`;
    const start = new Date(Date.now() + 5 * 86_400_000);
    const url = writeFeed(`status-${stamp}.ics`, [
      {
        uid: `class-${stamp}`,
        title: `Feed class ${stamp}`,
        start,
        end: new Date(start.getTime() + 3_600_000),
      },
    ]);
    const source = await createFeed(request, name, url);
    try {
      await waitForRefresh(request, source.id, "imported");
      await page.goto("/status");
      const feed = page
        .getByRole("region", { name: "Calendar sources" })
        .getByRole("listitem")
        .filter({ hasText: name });
      await expect(feed).toContainText("Refreshing normally.");
      await expect(feed).toContainText(/Last refreshimported, 1 added \(/);
      await expect(feed).not.toContainText("Failed in a row");

      // Renamed by the owner in Calendar (the source's name is theirs):
      // Status shows the new name at once.
      const renamed = `${name} renamed`;
      const patched = await request.patch(
        `/api/calendar/v1/sources/${source.sourceId}`,
        { data: { name: renamed } },
      );
      expect(patched.status(), await patched.text()).toBe(200);
      await page.reload();
      await expect(
        page
          .getByRole("region", { name: "Calendar sources" })
          .getByRole("listitem")
          .filter({ hasText: renamed }),
      ).toContainText("Refreshing normally.");
    } finally {
      await deleteFeed(request, source.id);
    }
  });
});

test.describe("Reminder delivery on Status", () => {
  test.skip(
    !process.env.LIFE_OS_E2E_URL,
    "set LIFE_OS_E2E_URL to run against the isolated stack",
  );

  /** A check's part, by its title. */
  function part(page: Page, check: string, title: string) {
    return page
      .getByRole("region", { name: check })
      .getByRole("listitem")
      .filter({
        has: page.getByRole("heading", { level: 3, name: title, exact: true }),
      });
  }

  test("Calendar's, Tasks', and Food's reminder senders show their last check", async ({
    page,
  }) => {
    // The three services get NOTIFICATIONS_SERVICE_URL on the verification
    // stack and check for due reminders every 30 s.
    await page.goto("/status");
    const events = part(page, "Calendar sources", "Event reminders");
    const tasks = part(page, "Notifications", "Task reminders");
    const food = part(page, "Notifications", "Food reminders");
    for (const sender of [events, tasks, food]) {
      await expect(sender).toContainText("OK");
      await expect(sender).toContainText(
        /Sending normally\.|Running; nothing sent yet\./,
      );
      await expect(sender).toContainText(/Last check\d+ \w+ \d{4}, \d\d:\d\d/);
      await expect(sender).toContainText("Last sent");
      await expect(sender).toContainText("Check interval30 s");
    }
    await expect(tasks).toContainText(
      /Schedulesummary .*, evening list \d\d:\d\d \(/,
    );
    await expect(tasks).toContainText("Timed Tasksreminded");
    await expect(food).toContainText(/Expiry reminderdaily at \d\d:\d\d \(/);
  });
});

test.describe("Server on Status @server-health", () => {
  test.skip(
    !process.env.LIFE_OS_E2E_URL,
    "set LIFE_OS_E2E_URL to run against the isolated stack",
  );

  // The verification stack's host-health reports a made-up server
  // (HOST_HEALTH_FIXTURE=healthy), never the host it runs on.
  test("the Server card shows the made-up server, with a day of memory and Life OS's use", async ({
    page,
  }) => {
    await page.goto("/status");
    const card = page.getByRole("region", { name: "Server" });
    await expect(card).toContainText("OK");
    await expect(card).toContainText(
      /Running normally: [\d.]+ GiB of memory available, a 15-minute load of 1\.3 on 6 CPUs\./,
    );
    await expect(card).toContainText(
      "Sourcea made-up server (HOST_HEALTH_FIXTURE)",
    );
    await expect(card).toContainText("Disk root125 GiB free of 232 GiB (54%)");
    await expect(card).toContainText(
      "Containers5 running, 0 unhealthy, 0 restarting (6 in all)",
    );
    await expect(card).toContainText(
      "Most memorylifeos-whisper-1 1.9 GiB, homeassistant 600 MiB, n8n 420 MiB",
    );
    await expect(card).toContainText("Up for12 days");
    // How much of it the made-up Life OS uses: facts, never a finding.
    await expect(card).toContainText(
      "Life OSCPU 0.4 cores (7%) · RAM 2.3 GiB (30%)",
    );
    await expect(card).toContainText(
      "Life OS serviceswhisper 1.9 GiB, postgres 310 MiB, dashboard 180 MiB",
    );
    // The made-up server's memory follows the time of day, so the rest of
    // its use is sometimes under 1 GiB (shown in MiB).
    await expect(card).toContainText(
      /Everything elseCPU 1\.0 cores \(17%\) · RAM [\d.]+ [GM]iB \(\d+%\)/,
    );
    await expect(card).not.toContainText("Tests running");

    // The sparklines: captions that need no hovering, and a tooltip that
    // follows the pointer and the arrow keys.
    const chart = card.getByRole("img", {
      name: "Memory available, last 24 h",
    });
    await expect(card.locator("figcaption")).toHaveText([
      /^Memory available, last 24 h: lowest [\d.]+ GiB at \d\d:\d\d, now [\d.]+ GiB; warning below 1\.2 GiB\.$/,
      /^Life OS memory, last 24 h: highest 2\.7 GiB at \d\d:\d\d, now 2\.3 GiB\.$/,
      /^Life OS CPU, last 24 h: highest 1\.2 cores at \d\d:\d\d, now 0\.4 cores\.$/,
    ]);
    await chart.hover({ position: { x: 10, y: 20 } });
    await expect(card.getByRole("tooltip")).toHaveText(
      /^[\d.]+ GiB \d\d:\d\d$/,
    );
    await page.mouse.move(0, 0);
    await expect(card.getByRole("tooltip")).toHaveCount(0);
    await chart.focus();
    await expect(card.getByRole("tooltip")).toBeVisible();
    await page.keyboard.press("ArrowLeft");
    await page.keyboard.press("Escape");
    await expect(card.getByRole("tooltip")).toHaveCount(0);
  });
});

test.describe("Backups on Status", () => {
  test.skip(
    !process.env.LIFE_OS_E2E_URL,
    "set LIFE_OS_E2E_URL to run against the isolated stack",
  );
  test.setTimeout(240_000);

  test("the Backups card shows the backups, and Back up now runs one", async ({
    page,
  }) => {
    await page.goto("/status");
    const card = page.getByRole("region", { name: "Backups" });
    await expect(card).toContainText("Next backup");
    await expect(card).toContainText("Retention7 daily, 4 weekly, 12 monthly");
    await expect(card).toContainText("Free disk");

    // A catch-up backup or restore check may be running on a fresh stack:
    // then the button follows that one (409) instead of starting another.
    const button = card.getByRole("button", { name: "Back up now" });
    await expect(button).toBeEnabled({ timeout: 120_000 });
    await button.click();
    await expect(card).toContainText(
      /Backed up \d+ databases in [\d.]+ s\.|Finished; the card shows the result\./,
      { timeout: 180_000 },
    );
    await expect(
      card.getByRole("button", { name: "Back up now" }),
    ).toBeEnabled();
    // The page was refreshed: the card reports a backup from just now.
    await expect(card).toContainText(
      /Last backup.*\((less than a minute|\d+ min) ago\)/,
    );
    await expect(card).toContainText("Last restore check");
  });
});

test.describe("Status card layout", () => {
  test.skip(
    !process.env.LIFE_OS_E2E_URL,
    "set LIFE_OS_E2E_URL to run against the isolated stack",
  );

  // The order of the checks in the runtime status; Calendar sources and
  // Notifications are left out when the Dashboard does not ask them.
  const checkOrder = [
    "Automatic processing",
    "Inbox backlog",
    "Voice transcription",
    "Calendar sources",
    "Notifications",
    "Bank connection",
    "Backups",
    "Server",
    "Services",
  ];

  test("Notifications is not set up on the verification stack, and that is fine", async ({
    page,
  }) => {
    // tools/verify-compose never passes the owner's delivery channels.
    await page.goto("/status");
    const card = page.getByRole("region", { name: "Notifications" });
    await expect(card).toContainText("Not set up");
    await expect(card).toContainText(
      "No delivery channel is set up: notifications are listed in Life OS only.",
    );
    await page.goto("/");
    await expect(
      page.getByRole("link", { name: /^System status:.*delivery channel/ }),
    ).toHaveCount(0);
  });

  /** The grid holding the check cards. */
  function cardGrid(page: Page) {
    return page
      .getByRole("region", { name: "Inbox backlog" })
      .locator("xpath=..");
  }

  /** Each card in page order: its title, box, and the height its content needs. */
  function cardBoxes(page: Page) {
    return cardGrid(page).evaluate((grid) => ({
      gridTop: grid.getBoundingClientRect().top,
      cards: Array.from(grid.children, (card) => {
        const box = card.getBoundingClientRect();
        const end = card.lastElementChild!.getBoundingClientRect().bottom;
        return {
          title: card.querySelector('[data-slot="card-title"]')!.textContent!,
          left: Math.round(box.left),
          top: box.top,
          bottom: box.bottom,
          height: box.height,
          // Down to the end of its last part, plus its bottom padding.
          content:
            end - box.top + parseFloat(getComputedStyle(card).paddingBottom),
        };
      }),
    }));
  }

  test("cards sit tightly in two columns that end level, in the checks' order", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto("/status");
    await expect(cardGrid(page)).toHaveAttribute("data-packed");

    await expect(async () => {
      const { gridTop, cards } = await cardBoxes(page);
      const titles = cards.map((card) => card.title);
      expect(titles.length).toBeGreaterThanOrEqual(4);
      expect(titles).toEqual(
        checkOrder.filter((title) => titles.includes(title)),
      );
      // Reading top to bottom, then left to right, follows the same order.
      const reading = [...cards]
        .sort((a, b) => a.top - b.top || a.left - b.left)
        .map((card) => card.title);
      expect(reading).toEqual(titles);

      const lefts = [...new Set(cards.map((card) => card.left))];
      expect(lefts).toHaveLength(2);
      const columns = lefts.map((left) =>
        cards.filter((card) => card.left === left),
      );
      for (const column of columns) {
        // Each card starts one 1rem gap under the previous card in its column.
        expect(Math.abs(column[0].top - gridTop)).toBeLessThanOrEqual(1);
        for (let index = 1; index < column.length; index++) {
          const gap = column[index].top - column[index - 1].bottom;
          expect(gap, `gap above ${column[index].title}`).toBeGreaterThan(15);
          expect(gap, `gap above ${column[index].title}`).toBeLessThan(18);
        }
        // Only the column's last card may be taller than its content.
        for (const card of column.slice(0, -1)) {
          expect(
            Math.abs(card.height - card.content),
            `empty space in ${card.title}`,
          ).toBeLessThanOrEqual(2);
        }
      }
      // Inbox backlog is short: unless it ends its column, it has no empty
      // space under its content.
      const inbox = cards.find((card) => card.title === "Inbox backlog")!;
      const inboxColumn = columns.find((column) => column.includes(inbox))!;
      if (inboxColumn.at(-1) !== inbox) {
        expect(inbox.height - inbox.content).toBeLessThanOrEqual(2);
      }
      const [left, right] = columns.map((column) => column.at(-1)!.bottom);
      expect(Math.abs(left - right), "column bottoms").toBeLessThanOrEqual(2);
    }).toPass({ timeout: 10_000 });

    // On a phone the cards are one column, in the same order.
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(cardGrid(page)).not.toHaveAttribute("data-packed");
    await expect(async () => {
      const { cards } = await cardBoxes(page);
      const titles = cards.map((card) => card.title);
      expect(titles).toEqual(
        checkOrder.filter((title) => titles.includes(title)),
      );
      expect(new Set(cards.map((card) => card.left)).size).toBe(1);
      for (let index = 1; index < cards.length; index++) {
        expect(cards[index].top).toBeGreaterThan(cards[index - 1].bottom);
      }
    }).toPass({ timeout: 10_000 });
  });
});
