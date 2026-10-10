import { expect, type Page, type Route, test } from "./fixtures";

// The Google Calendar connection (P11-01). The verification stack never
// talks to Google and has no Google client (tools/verify-compose blanks
// LIFE_OS_GOOGLE_*), so the first test checks the real "not set up" answer
// and the others stub the browser's Calendar API calls for Google with
// page.route: "Google" sends the browser straight back to the callback with
// a code, as it does after consent. The Status page's Google part is read on
// the server and is covered by unit tests (runtime-status-google.test.ts).

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

const OWNER = "owner@example.com";
const SCOPES = [
  "https://www.googleapis.com/auth/calendar.calendarlist.readonly",
  "https://www.googleapis.com/auth/calendar.events",
  "https://www.googleapis.com/auth/calendar.app.created",
];

type Calendar = {
  id: string;
  googleCalendarId: string;
  summary: string;
  color: string | null;
  accessRole: string;
  primary: boolean;
  syncEnabled: boolean;
  createdAt: string;
  updatedAt: string;
};

const calendar = (
  id: string,
  summary: string,
  extra: Partial<Calendar> = {},
): Calendar => ({
  id,
  googleCalendarId: `${summary.toLowerCase()}@group.calendar.google.com`,
  summary,
  color: "#9fe1e7",
  accessRole: "writer",
  primary: false,
  syncEnabled: false,
  createdAt: "2026-10-10T10:00:00Z",
  updatedAt: "2026-10-10T10:00:00Z",
  ...extra,
});

/**
 * A stand-in for Calendar's Google endpoints, as the browser sees them
 * through /api/calendar. It records what the browser sent.
 */
async function stubGoogle(page: Page, baseURL: string) {
  const state = {
    status: "not_connected" as "not_connected" | "connected" | "expired",
    calendars: [] as Calendar[],
    connectBodies: [] as unknown[],
    patches: [] as unknown[],
    disconnected: 0,
    actors: new Set<string>(),
  };
  const connection = () => ({
    configured: true,
    status: state.status,
    accountEmail: state.status === "not_connected" ? null : OWNER,
    scopes: state.status === "not_connected" ? [] : SCOPES,
    missingScopes: [],
    connectedAt:
      state.status === "not_connected" ? null : "2026-10-10T10:00:00Z",
    lastError:
      state.status === "expired"
        ? "Google refused the request (invalid_grant: Token has been expired or revoked.): reconnect Google in Calendar."
        : null,
  });
  const json = (route: Route, body: unknown, status = 200) =>
    route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(body),
    });

  await page.route("**/api/calendar/v1/google/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/calendar", "");
    const method = request.method();
    if (method !== "GET") {
      state.actors.add(request.headers()["x-life-os-actor"] ?? "");
    }
    if (method === "GET" && path === "/v1/google/connection") {
      return json(route, connection());
    }
    if (method === "GET" && path === "/v1/google/calendars") {
      return json(route, {
        items: state.status === "not_connected" ? [] : state.calendars,
      });
    }
    if (method === "POST" && path === "/v1/google/connection/authorize") {
      // Google's consent page, answered at once.
      return json(route, {
        url: `${baseURL}/calendar/google/callback?state=fake-state&code=4%2F0fake-code&scope=${encodeURIComponent(SCOPES.join(" "))}`,
      });
    }
    if (method === "POST" && path === "/v1/google/connection") {
      const body = request.postDataJSON() as { code: string; state: string };
      state.connectBodies.push(body);
      if (body.state !== "fake-state") {
        return json(
          route,
          {
            error: {
              code: "invalid_state",
              message: "this Google sign-in expired or was already used",
            },
          },
          400,
        );
      }
      state.status = "connected";
      state.calendars = [
        calendar("c1", "Owner", {
          googleCalendarId: OWNER,
          accessRole: "owner",
          primary: true,
          syncEnabled: true,
        }),
        calendar("c2", "Family"),
        calendar("c3", "Holidays in Germany", {
          accessRole: "reader",
          color: null,
        }),
      ];
      return json(route, connection());
    }
    if (method === "PATCH" && path.startsWith("/v1/google/calendars/")) {
      const id = path.split("/").at(-1);
      const body = request.postDataJSON() as { syncEnabled: boolean };
      state.patches.push({ id, ...body });
      state.calendars = state.calendars.map((item) =>
        item.id === id ? { ...item, syncEnabled: body.syncEnabled } : item,
      );
      return json(
        route,
        state.calendars.find((item) => item.id === id),
      );
    }
    if (method === "POST" && path === "/v1/google/calendars/refresh") {
      state.calendars = [
        ...state.calendars,
        calendar("c4", "Work", { accessRole: "reader" }),
      ];
      return json(route, { items: state.calendars });
    }
    if (method === "DELETE" && path === "/v1/google/connection") {
      state.disconnected++;
      state.status = "not_connected";
      state.calendars = [];
      return route.fulfill({ status: 204 });
    }
    return route.fallback();
  });
  return state;
}

const googleDialog = (page: Page) =>
  page.getByRole("dialog", { name: "Google Calendar" });

test.describe("Google Calendar connection", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("without Google settings the dialog says what the server needs", async ({
    page,
  }) => {
    await page.goto("/calendar");
    await page.getByRole("button", { name: "Google Calendar" }).click();
    const dialog = googleDialog(page);
    await expect(dialog).toContainText("not set up on this server");
    await expect(dialog).toContainText("LIFE_OS_GOOGLE_CLIENT_ID");
    await expect(
      dialog.getByRole("button", { name: "Connect Google" }),
    ).toHaveCount(0);
  });

  test("connects, chooses calendars, refreshes, and disconnects", async ({
    page,
    baseURL,
  }) => {
    const google = await stubGoogle(page, baseURL!);
    await page.goto("/calendar?view=month");
    await page.getByRole("button", { name: "Google Calendar" }).click();
    const dialog = googleDialog(page);
    await expect(dialog).toContainText("No Google account is connected");

    // Connect Google → "Google" → the callback → back on the Calendar with
    // the dialog open.
    await dialog.getByRole("button", { name: "Connect Google" }).click();
    await page.waitForURL(/\/calendar$/);
    await expect(dialog).toContainText("Google Calendar is connected");
    await expect(dialog).toContainText(`Connected as ${OWNER}`);
    expect(google.connectBodies).toEqual([
      { code: "4/0fake-code", state: "fake-state" },
    ]);
    // The marker is gone, so a reload does not reopen the dialog.
    expect(new URL(page.url()).search).toBe("");

    const list = dialog.getByTestId("google-calendar-list");
    await expect(list.getByRole("checkbox")).toHaveCount(3);
    await expect(list.getByRole("checkbox", { name: /Owner/ })).toBeChecked();
    await expect(dialog).toContainText("1 of 3 calendars chosen to sync");
    await list.getByRole("checkbox", { name: /Family/ }).check();
    await expect(dialog).toContainText("2 of 3 calendars chosen to sync");
    expect(google.patches).toEqual([{ id: "c2", syncEnabled: true }]);

    await dialog.getByRole("button", { name: "Refresh list" }).click();
    await expect(
      list.getByRole("checkbox", { name: /Work/ }),
    ).not.toBeChecked();
    await expect(dialog).toContainText("The calendar list is up to date");

    // Disconnect asks first.
    await dialog
      .getByRole("button", { name: "Disconnect", exact: true })
      .click();
    await expect(dialog).toContainText(`Disconnect ${OWNER}?`);
    await dialog.getByRole("button", { name: "Keep it" }).click();
    expect(google.disconnected).toBe(0);
    await dialog
      .getByRole("button", { name: "Disconnect", exact: true })
      .click();
    await dialog.getByRole("button", { name: "Disconnect Google" }).click();
    await expect(dialog).toContainText("Google Calendar is disconnected");
    await expect(
      dialog.getByRole("button", { name: "Connect Google" }),
    ).toBeVisible();
    expect(google.disconnected).toBe(1);
    // Every write went out as the owner.
    expect([...google.actors]).toEqual(["user"]);
  });

  test("an expired connection asks to reconnect", async ({ page, baseURL }) => {
    const google = await stubGoogle(page, baseURL!);
    google.status = "expired";
    google.calendars = [calendar("c1", "Owner", { primary: true })];
    await page.goto("/calendar");
    await page.getByRole("button", { name: "Google Calendar" }).click();
    const dialog = googleDialog(page);
    await expect(dialog.getByRole("alert")).toContainText(
      `Google access for ${OWNER} expired or was revoked`,
    );
    await expect(dialog).toContainText("invalid_grant");
    await expect(
      dialog.getByRole("button", { name: "Reconnect Google" }),
    ).toBeVisible();
  });

  test("the callback explains a refusal and drops the code from the address bar", async ({
    page,
    baseURL,
  }) => {
    await stubGoogle(page, baseURL!);
    await page.goto("/calendar/google/callback?error=access_denied&state=x");
    await expect(page.getByRole("alert")).toContainText(
      "You did not allow Life OS to use your Google Calendar",
    );
    await expect(page).toHaveURL(/\/calendar\/google\/callback$/);

    await page.goto("/calendar/google/callback?code=4%2F0old&state=used");
    await expect(page.getByRole("alert")).toContainText(
      "This sign-in expired or was already used",
    );
    await expect(page).toHaveURL(/\/calendar\/google\/callback$/);
    await page.getByRole("link", { name: "Back to Calendar" }).click();
    await page.waitForURL(/\/calendar$/);
  });

  test("the consent screen's privacy and terms pages are there", async ({
    page,
  }) => {
    await page.goto("/privacy");
    await expect(
      page.getByRole("heading", { name: "Privacy policy" }),
    ).toBeVisible();
    await expect(page.getByRole("main")).toContainText("Limited Use");
    await page.goto("/terms");
    await expect(
      page.getByRole("heading", { name: "Terms of service" }),
    ).toBeVisible();
  });
});
