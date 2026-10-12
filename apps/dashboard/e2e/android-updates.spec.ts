import { createHash } from "node:crypto";

import { expect, test } from "@playwright/test";

// The Android apps' downloads at /android/, served by the gateway from the
// releases directory (docs/operations/android-releases.md). tools/browser-e2e
// publishes a made-up release into the verification stack's own directory
// and sets LIFE_OS_E2E_ANDROID_RELEASE.
const enabled = Boolean(
  process.env.LIFE_OS_E2E_URL && process.env.LIFE_OS_E2E_ANDROID_RELEASE,
);

type AppRelease = {
  versionCode: number;
  versionName: string;
  minSdk: number;
  sha256: string;
  size: number;
  url: string;
  publishedAt: string;
  notes: string;
};

test.describe("Android downloads", () => {
  test.skip(
    !enabled,
    "run through tools/browser-e2e, which publishes a release for the stack",
  );

  test("serves the manifest, the APKs, and the download page", async ({
    page,
    request,
  }) => {
    const response = await request.get("/android/updates");
    expect(response.status()).toBe(200);
    expect(response.headers()["content-type"]).toBe("application/json");
    expect(response.headers()["cache-control"]).toBe("no-cache");
    const manifest = (await response.json()) as {
      formatVersion: number;
      apps: { phone: AppRelease; wear: AppRelease };
    };
    expect(manifest.formatVersion).toBe(1);
    const { phone, wear } = manifest.apps;
    expect(phone.notes).toBe("Browser test release");

    for (const [app, release] of Object.entries(manifest.apps)) {
      expect(release.url).toBe(
        `/android/lifeos-${app}-${release.versionName}-${release.versionCode}.apk`,
      );
      const apk = await request.get(release.url);
      expect(apk.status(), release.url).toBe(200);
      expect(apk.headers()["content-type"]).toBe(
        "application/vnd.android.package-archive",
      );
      expect(apk.headers()["content-disposition"]).toBe("attachment");
      expect(apk.headers()["cache-control"]).toBe(
        "private, max-age=31536000, immutable",
      );
      const body = await apk.body();
      expect(body.length).toBe(release.size);
      expect(createHash("sha256").update(body).digest("hex")).toBe(
        release.sha256,
      );
    }
    for (const path of [
      "/android/lifeos-phone-0.0.0-999999.apk",
      "/android/index.html",
      "/android/release.json",
    ]) {
      expect((await request.get(path)).status(), path).toBe(404);
    }

    // The page: the phone APK as the big download, the watch's apart.
    await page.goto("/android/");
    const download = page.getByRole("link", {
      name: `Download Life OS ${phone.versionName}`,
    });
    await expect(download).toHaveAttribute("href", phone.url);
    await expect(
      page.getByRole("heading", { name: "First install" }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", {
        name: `Life OS for Wear OS ${wear.versionName}`,
      }),
    ).toHaveAttribute("href", wear.url);
    const [file] = await Promise.all([
      page.waitForEvent("download"),
      download.click(),
    ]);
    expect(file.suggestedFilename()).toBe(phone.url.split("/").pop());
  });
});
