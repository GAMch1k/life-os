import { expect, type Page, type Route, test } from "./fixtures";

// The bank connection (P13-03) on the Finance page. The verification stack
// never talks to a bank: tools/verify-compose blanks the Enable Banking
// settings, mounts no key, and points banking at an address that resolves
// nowhere. So the first test checks the real "not set up" answer, and the
// others stub the browser's banking calls (/api/integrations/banking) with
// page.route: "the bank" sends the browser straight back to the callback
// with a code, as it does after consent. The Status page's bank part is read
// on the server and is covered by unit tests (runtime-status-banking.test.ts).

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

type Connection = Record<string, unknown> & { id: string; status: string };

const SCHEDULE = {
  times: ["06:00", "12:00", "18:00", "23:00"],
  timeZone: "Europe/Berlin",
  unattendedPerDay: 4,
  manualSyncMinutes: 10,
  historyDays: 730,
};

/** The history read banking runs after a bank login, under way. */
const historyRunning = () => ({
  status: "running",
  startedAt: new Date().toISOString(),
  finishedAt: null,
  transactions: 0,
  error: null,
});

const connected = (extra: Partial<Connection> = {}): Connection => ({
  id: "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10",
  provider: "enablebanking",
  institution: "Deutsche Bank",
  country: "DE",
  status: "connected",
  consentValidUntil: new Date(Date.now() + 179 * 86_400_000).toISOString(),
  accounts: [
    {
      id: "iban-0123",
      name: "Das Junge Konto",
      currency: "EUR",
      kind: "bank",
      ibanMasked: "DE89…3000",
    },
  ],
  lastSyncAt: new Date().toISOString(),
  lastSuccessAt: new Date().toISOString(),
  nextSyncAt: new Date(Date.now() + 3_600_000).toISOString(),
  consecutiveFailures: 0,
  lastError: null,
  lastCounts: {
    accounts: { created: 1, updated: 0 },
    transactions: {
      created: 175,
      updated: 0,
      unchanged: 0,
      removedPending: 0,
      skipped: 0,
    },
    fetched: 175,
    ignored: 0,
  },
  unattendedReadsToday: 0,
  manualSyncAvailableAt: null,
  waitingForFinance: false,
  transactionsSince: "2026-07-14",
  historyRead: null,
  connectedAt: new Date().toISOString(),
  disconnectedAt: null,
  createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(),
  ...extra,
});

/** A stand-in for the banking integration, as the browser sees it. */
async function stubBanking(page: Page, baseURL: string) {
  const state = {
    items: [] as Connection[],
    connectBodies: [] as unknown[],
    failureBodies: [] as unknown[],
    failure: null as null | Record<string, unknown>,
    syncs: 0,
    disconnected: 0,
    actors: new Set<string>(),
  };
  const json = (route: Route, body: unknown, status = 200) =>
    route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(body),
    });

  await page.route("**/api/integrations/banking/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(
      "/api/integrations/banking",
      "",
    );
    const method = request.method();
    if (method !== "GET") {
      state.actors.add(request.headers()["x-life-os-actor"] ?? "");
    }
    if (method === "GET" && path === "/v1/connections") {
      return json(route, {
        configured: true,
        setupProblem: null,
        schedule: SCHEDULE,
        items: state.items,
        nextCursor: null,
        lastConsentFailure: state.failure,
      });
    }
    if (method === "POST" && path === "/v1/connections/authorize") {
      // The bank's consent page, answered at once.
      return json(route, {
        url: `${baseURL}/finance/bank/callback?state=fake-state&code=fake-code`,
      });
    }
    if (method === "POST" && path === "/v1/connections") {
      const body = request.postDataJSON() as { code: string; state: string };
      state.connectBodies.push(body);
      if (body.state !== "fake-state") {
        return json(
          route,
          {
            error: {
              code: "invalid_state",
              message: "this bank sign-in expired or was already used",
            },
          },
          400,
        );
      }
      // Banking answers after the first sync, with the history read under
      // way in the background.
      state.items = [connected({ historyRead: historyRunning() })];
      return json(route, state.items[0]);
    }
    if (method === "POST" && path === "/v1/connections/consent-failure") {
      const body = request.postDataJSON() as {
        error: string;
        errorDescription?: string;
      };
      state.failureBodies.push(body);
      state.failure = {
        at: new Date().toISOString(),
        error: body.error,
        description: body.errorDescription ?? null,
        institution: "Deutsche Bank",
        stateMatched: true,
      };
      return route.fulfill({ status: 204 });
    }
    if (method === "POST" && path.endsWith("/sync")) {
      state.syncs++;
      state.items = [
        connected({
          manualSyncAvailableAt: new Date(Date.now() + 600_000).toISOString(),
          lastCounts: {
            accounts: { created: 0, updated: 1 },
            transactions: {
              created: 2,
              updated: 0,
              unchanged: 173,
              removedPending: 1,
              skipped: 0,
            },
            fetched: 175,
            ignored: 0,
          },
        }),
      ];
      return json(route, state.items[0]);
    }
    if (method === "DELETE" && path.startsWith("/v1/connections/")) {
      state.disconnected++;
      state.items = state.items.map((item) => ({
        ...item,
        status: "disconnected",
      }));
      return route.fulfill({ status: 204 });
    }
    return route.fallback();
  });
  return state;
}

const bankCard = (page: Page) => page.getByTestId("bank-connection");

test.describe("Bank connection", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("without Enable Banking settings the card says what the server needs", async ({
    page,
  }) => {
    await page.goto("/finance");
    const card = bankCard(page);
    await expect(card).toContainText("not set up on this server");
    await expect(card).toContainText("LIFE_OS_ENABLE_BANKING_APP_ID");
    await expect(
      card.getByRole("button", { name: "Connect Deutsche Bank" }),
    ).toHaveCount(0);
  });

  test("connects, syncs now, and disconnects", async ({ page, baseURL }) => {
    const bank = await stubBanking(page, baseURL!);
    await page.goto("/finance");
    const card = bankCard(page);
    await expect(card).toContainText("No bank is connected");

    // Connect → "the bank" → the callback → back on Finance.
    await card.getByRole("button", { name: "Connect Deutsche Bank" }).click();
    await page.waitForURL(/\/finance$/);
    await expect(card).toContainText("Deutsche Bank is connected.");
    expect(bank.connectBodies).toEqual([
      { code: "fake-code", state: "fake-state" },
    ]);
    // The marker is gone, so a reload does not repeat the notice.
    expect(new URL(page.url()).search).toBe("");
    await expect(card.getByTestId("bank-accounts")).toContainText(
      "Das Junge Konto (DE89…3000)",
    );
    await expect(card.getByTestId("bank-sync")).toContainText(
      /^Synced .* · 175 new transactions$/,
    );
    await expect(card).toContainText("Access valid until");
    await expect(card).toContainText("Connected");

    await card.getByRole("button", { name: "Sync now" }).click();
    await expect(card.getByTestId("bank-sync")).toContainText(
      "2 new transactions",
    );
    await expect(card.getByRole("button", { name: "Sync now" })).toBeDisabled();
    await expect(card).toContainText("Synced moments ago");
    expect(bank.syncs).toBe(1);

    // Disconnect asks first.
    await card.getByRole("button", { name: "Disconnect", exact: true }).click();
    await expect(card).toContainText("Disconnect Deutsche Bank?");
    await card.getByRole("button", { name: "Keep it" }).click();
    expect(bank.disconnected).toBe(0);
    await card.getByRole("button", { name: "Disconnect", exact: true }).click();
    await card
      .getByRole("button", { name: "Disconnect Deutsche Bank" })
      .click();
    await expect(card).toContainText("Deutsche Bank is disconnected");
    await expect(
      card.getByRole("button", { name: "Connect Deutsche Bank" }),
    ).toBeVisible();
    expect(bank.disconnected).toBe(1);
    // Every write went out as the owner.
    expect([...bank.actors]).toEqual(["user"]);
  });

  test("an expired consent asks to reconnect; one expiring soon offers it", async ({
    page,
    baseURL,
  }) => {
    const bank = await stubBanking(page, baseURL!);
    bank.items = [
      connected({
        status: "expired",
        nextSyncAt: null,
        consentValidUntil: new Date(Date.now() - 3_600_000).toISOString(),
        lastError:
          "Deutsche Bank access expired. Reconnect the bank to keep syncing.",
      }),
    ];
    await page.goto("/finance");
    const card = bankCard(page);
    await expect(card.getByRole("alert")).toContainText(
      "Deutsche Bank access expired",
    );
    await expect(card.getByRole("button", { name: "Reconnect" })).toBeVisible();
    await expect(card.getByRole("button", { name: "Sync now" })).toHaveCount(0);

    bank.items = [
      connected({
        consentValidUntil: new Date(Date.now() + 3 * 86_400_000).toISOString(),
      }),
    ];
    await page.reload();
    await expect(card).toContainText("Expires soon");
    await expect(card).toContainText(/Access ends in \d days/);
    await expect(card.getByRole("button", { name: "Reconnect" })).toBeVisible();
    await expect(card.getByRole("button", { name: "Sync now" })).toBeEnabled();
  });

  test("says how far back the transactions go and loads older history after a bank login", async ({
    page,
    baseURL,
  }) => {
    const bank = await stubBanking(page, baseURL!);
    bank.items = [connected()];
    await page.goto("/finance");
    const card = bankCard(page);
    await expect(card.getByTestId("bank-since")).toHaveText(
      "Transactions since 14 Jul 2026",
    );
    await expect(card.getByTestId("bank-history")).toHaveCount(0);

    // The action explains why first; "Not now" leaves everything as it was.
    await card.getByRole("button", { name: "Load older history" }).click();
    const help = card.getByTestId("bank-history-help");
    await expect(help).toContainText(
      "Deutsche Bank shares transactions older than 90 days only right after you log in at the bank.",
    );
    await expect(help).toContainText("up to 2 years of older history");
    await card.getByRole("button", { name: "Not now" }).click();
    await expect(help).toHaveCount(0);
    expect(bank.connectBodies).toEqual([]);

    // Then it reconnects: "the bank" → the callback → back on Finance.
    await card.getByRole("button", { name: "Load older history" }).click();
    await card.getByRole("button", { name: "Log in at Deutsche Bank" }).click();
    // The page is on /finance already, so wait for the card, not the URL.
    // Banking reads the history in the background: the card says so.
    await expect(card.getByTestId("bank-history")).toHaveText(
      "Loading older history from Deutsche Bank…",
    );
    expect(new URL(page.url()).pathname).toBe("/finance");
    expect(bank.connectBodies).toEqual([
      { code: "fake-code", state: "fake-state" },
    ]);
    await expect(
      card.getByRole("button", { name: "Load older history" }),
    ).toHaveCount(0);

    // Done: the card checks on it by itself.
    bank.items = [
      connected({
        transactionsSince: "2024-10-14",
        historyRead: {
          ...historyRunning(),
          status: "completed",
          finishedAt: new Date().toISOString(),
          transactions: 1204,
        },
      }),
    ];
    await expect(card.getByTestId("bank-since")).toHaveText(
      "Transactions since 14 Oct 2024",
      { timeout: 20_000 },
    );
    await expect(card.getByTestId("bank-history")).toContainText(
      /^Older history loaded .* · 1,204 transactions$/,
    );
    await expect(
      card.getByRole("button", { name: "Load older history" }),
    ).toBeVisible();
  });

  test("the callback explains a refusal, keeps it in banking, and drops the code from the address bar", async ({
    page,
    baseURL,
  }) => {
    const bank = await stubBanking(page, baseURL!);
    await page.goto(
      "/finance/bank/callback?error=server_error&error_description=Internal%20error&state=x",
    );
    await expect(
      page.locator("#main-content").getByRole("alert"),
    ).toContainText("The bank did not complete the consent (server_error).");
    await expect(page).toHaveURL(/\/finance\/bank\/callback$/);
    // Reported to banking as the owner, without a code.
    await expect
      .poll(() => bank.failureBodies)
      .toEqual([
        {
          error: "server_error",
          state: "x",
          errorDescription: "Internal error",
        },
      ]);
    expect([...bank.actors]).toEqual(["user"]);
    // Finance shows the last failure next to Connect.
    await page.goto("/finance");
    await expect(
      bankCard(page).getByTestId("bank-consent-failure"),
    ).toContainText(
      /^Last attempt failed on .*: server_error — Internal error$/,
    );

    await page.goto("/finance/bank/callback?error=access_denied&state=y");
    await expect(
      page.locator("#main-content").getByRole("alert"),
    ).toContainText("You did not give consent at the bank");

    await page.goto("/finance/bank/callback?code=old&state=used");
    await expect(
      page.locator("#main-content").getByRole("alert"),
    ).toContainText("This consent expired or was already used");
    await expect(page).toHaveURL(/\/finance\/bank\/callback$/);
    await page.getByRole("link", { name: "Back to Finance" }).click();
    await page.waitForURL(/\/finance$/);
  });
});
