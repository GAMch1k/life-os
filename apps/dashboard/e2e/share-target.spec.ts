import { expect, test } from "./fixtures";

const enabled = Boolean(process.env.LIFE_OS_E2E_URL);

type InboxItem = {
  id: string;
  content: string;
  source: string;
  sourceMetadata?: Record<string, unknown>;
  mediaReferences?: string[];
};

test.describe("Install on the phone", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("serves the manifest with credentials, its icons, and the service worker", async ({
    page,
    request,
  }) => {
    const response = await request.get("/app.webmanifest");
    expect(response.status()).toBe(200);
    expect(response.headers()["content-type"]).toContain(
      "application/manifest+json",
    );
    const manifest = await response.json();
    expect(manifest).toMatchObject({
      name: "Life OS",
      short_name: "Life OS",
      start_url: "/",
      display: "standalone",
      share_target: {
        action: "/share",
        method: "POST",
        enctype: "multipart/form-data",
      },
    });
    const icons = manifest.icons as { src: string; sizes: string }[];
    expect(icons.map((icon) => icon.sizes)).toEqual(
      expect.arrayContaining(["192x192", "512x512"]),
    );
    for (const icon of icons) {
      const image = await request.get(icon.src);
      expect(image.status(), icon.src).toBe(200);
      expect(image.headers()["content-type"]).toBe("image/png");
    }
    const worker = await request.get("/sw.js");
    expect(worker.status()).toBe(200);
    expect(worker.headers()["content-type"]).toContain("javascript");

    // The page links the manifest so that the Access cookie goes with it,
    // and registers the worker.
    await page.goto("/");
    await expect(page.locator('link[rel="manifest"]')).toHaveAttribute(
      "crossorigin",
      "use-credentials",
    );
    await expect(page.locator('link[rel="manifest"]')).toHaveAttribute(
      "href",
      "/app.webmanifest",
    );
    await expect
      .poll(() =>
        page.evaluate(async () =>
          Boolean(await navigator.serviceWorker?.getRegistration("/")),
        ),
      )
      .toBe(true);
  });
});

test.describe("Share → Life OS", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("a shared text and link become an Inbox capture, then Saved to Inbox", async ({
    page,
    request,
  }) => {
    const stamp = Date.now();
    const text = `P16 shared note ${stamp}`;
    const url = `https://example.com/p16/${stamp}`;
    await page.goto("/share");
    await expect(page).toHaveURL(/\/share\/about$/);
    await expect(
      page.getByRole("heading", { name: "Share to Life OS" }),
    ).toBeVisible();

    // What the share sheet does: a multipart POST navigation of the app to
    // /share.
    await page.evaluate(
      ({ text, url }) => {
        const form = document.createElement("form");
        form.method = "POST";
        form.action = "/share";
        form.enctype = "multipart/form-data";
        for (const [name, value] of Object.entries({
          title: "Example page",
          text,
          url,
        })) {
          const input = document.createElement("input");
          input.type = "hidden";
          input.name = name;
          input.value = value;
          form.append(input);
        }
        document.body.append(form);
        form.submit();
      },
      { text, url },
    );
    await expect(page).toHaveURL(/\/share\/saved\?item=/);
    const saved = page.getByRole("region", { name: "Saved to Inbox" });
    await expect(saved).toBeVisible();
    await expect(saved).toContainText(text);

    const id = new URL(page.url()).searchParams.get("item");
    const response = await request.get(`/api/inbox/v1/inbox-items/${id}`);
    expect(response.status()).toBe(200);
    const item = (await response.json()) as InboxItem;
    expect(item.content).toBe(`Example page\n${text}\n${url}`);
    expect(item.source).toBe("web");
    expect(item.sourceMetadata).toMatchObject({
      shared: true,
      via: "share-target",
    });
    await expect(
      page.getByRole("link", { name: "Follow it in the Event Log" }),
    ).toHaveAttribute("href", `/activity?correlationId=${id}`);
  });

  test("a share from another site is refused, and an empty one saves nothing", async ({
    request,
  }) => {
    const foreign = await request.post("/share", {
      multipart: { text: "From elsewhere" },
      headers: {
        "Sec-Fetch-Site": "cross-site",
        Origin: "https://evil.example",
      },
      maxRedirects: 0,
    });
    expect(foreign.status()).toBe(403);

    const empty = await request.post("/share", {
      multipart: { text: "  " },
      headers: { "Sec-Fetch-Site": "none" },
      maxRedirects: 0,
    });
    expect(empty.status()).toBe(303);
    expect(empty.headers().location).toBe("/share/saved?error=empty");
  });
});

/** A 1×1 PNG, a photo as small as it gets. */
const PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
  "base64",
);

test.describe("Photos with a capture", () => {
  test.skip(!enabled, "set LIFE_OS_E2E_URL to run against the isolated stack");

  test("a shared photo is stored in Inbox, named by the capture, and shown", async ({
    page,
    request,
  }) => {
    const text = `P16 shared photo ${Date.now()}`;
    const response = await request.post("/share", {
      multipart: {
        text,
        images: { name: "photo.png", mimeType: "image/png", buffer: PNG },
      },
      headers: { "Sec-Fetch-Site": "none" },
      maxRedirects: 0,
    });
    expect(response.status()).toBe(303);
    const location = response.headers().location;
    expect(location).toMatch(/^\/share\/saved\?item=/);
    const id = new URLSearchParams(location.split("?")[1]).get("item");

    const item = (await (
      await request.get(`/api/inbox/v1/inbox-items/${id}`)
    ).json()) as InboxItem;
    expect(item.content).toBe(text);
    expect(item.mediaReferences).toHaveLength(1);
    const hash = item.mediaReferences![0].replace("inbox-media:", "");
    const image = await request.get(`/api/inbox/v1/media/${hash}`);
    expect(image.status()).toBe(200);
    expect(image.headers()["content-type"]).toBe("image/png");
    expect(Buffer.from(await image.body()).equals(PNG)).toBe(true);

    // The confirmation shows the photo; tapping it opens it whole.
    await page.goto(location);
    const saved = page.getByRole("region", { name: "Saved to Inbox" });
    const thumbnail = saved.getByRole("button", { name: "View photo 1 of 1" });
    await expect(thumbnail).toBeVisible();
    await expect
      .poll(() =>
        thumbnail
          .locator("img")
          .evaluate((img: HTMLImageElement) => img.naturalWidth),
      )
      .toBe(1);
    await thumbnail.click();
    await expect(
      page.getByRole("dialog").getByRole("img", { name: new RegExp(text) }),
    ).toBeVisible();
    await page.keyboard.press("Escape");

    // Inbox lists the capture with its photo, whatever section it is in.
    await page.goto("/inbox");
    await expect(
      page.locator(`img[src="/api/inbox/v1/media/${hash}"]`).first(),
    ).toBeVisible();
  });

  test("the composer attaches photos, refuses HEIC, and captures them", async ({
    page,
  }) => {
    const text = `P16 composer photo ${Date.now()}`;
    await page.goto("/");
    const gallery = page.locator('#capture input[type="file"][multiple]');
    await gallery.setInputFiles({
      name: "IMG_0001.HEIC",
      mimeType: "image/heic",
      buffer: Buffer.from("not really heic"),
    });
    await expect(
      page.getByRole("status").filter({ hasText: "HEIC" }),
    ).toBeVisible();

    await gallery.setInputFiles({
      name: "photo.png",
      mimeType: "image/png",
      buffer: PNG,
    });
    const attached = page.getByRole("list", { name: "Photos to capture" });
    await expect(attached.getByRole("listitem")).toHaveCount(1);
    await page.locator("#inbox-capture").fill(text);
    const created = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes("/api/inbox/v1/inbox-items"),
    );
    await page.getByRole("button", { name: "Capture", exact: true }).click();
    const response = await created;
    expect(response.status()).toBe(201);
    const item = (await response.json()) as InboxItem;
    expect(item.content).toBe(text);
    expect(item.mediaReferences).toHaveLength(1);
    await expect(attached).toHaveCount(0);
  });
});
