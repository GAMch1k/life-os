import assert from "node:assert/strict";
import test from "node:test";

import { SHARE_ACTION, SHARE_FIELDS } from "./share";
import { webAppManifest } from "./web-app";

test("the manifest makes Life OS an installable, standalone app", () => {
  const manifest = webAppManifest();
  assert.equal(manifest.name, "Life OS");
  assert.equal(manifest.short_name, "Life OS");
  assert.equal(manifest.start_url, "/");
  assert.equal(manifest.display, "standalone");
  assert.match(manifest.theme_color ?? "", /^#[0-9a-f]{6}$/);
  assert.match(manifest.background_color ?? "", /^#[0-9a-f]{6}$/);
  // Chrome installs with a 192 and a 512 px icon; maskable ones fill
  // Android's icon shapes.
  const icons = manifest.icons ?? [];
  for (const size of ["192x192", "512x512"]) {
    assert.ok(icons.some((icon) => icon.sizes === size && !icon.purpose));
    assert.ok(
      icons.some((icon) => icon.sizes === size && icon.purpose === "maskable"),
    );
  }
  for (const icon of icons) assert.match(icon.src, /^\/icons\/.+\.png$/);
});

test("the share target posts the shared fields to /share as multipart", () => {
  const target = webAppManifest().share_target;
  assert.equal(target?.action, SHARE_ACTION);
  assert.equal(target?.method, "POST");
  assert.equal(target?.enctype, "multipart/form-data");
  assert.equal(target?.params.title, SHARE_FIELDS.title);
  assert.equal(target?.params.text, SHARE_FIELDS.text);
  assert.equal(target?.params.url, SHARE_FIELDS.url);
});
