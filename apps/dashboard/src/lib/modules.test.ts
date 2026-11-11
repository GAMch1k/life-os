import assert from "node:assert/strict";
import test from "node:test";

import { MODULES } from "@/config/modules.generated";

import {
  ALL_PARTS,
  checkOn,
  enabledModules,
  featureOn,
  installedParts,
  moduleOffResponse,
  serviceOn,
} from "./modules";

const optional = MODULES.filter((entry) => entry.install === "optional");

test("the core always runs; it is the one module that does", () => {
  assert.deepEqual(
    MODULES.filter((entry) => entry.install === "core").map(
      (entry) => entry.id,
    ),
    ["core"],
  );
  assert.deepEqual(installedParts("").modules, ["core"]);
});

test("an empty LIFE_OS_MODULES is the core alone: no optional part shows", () => {
  const parts = installedParts("");
  assert.equal(featureOn("capture.voice", parts), false);
  assert.equal(featureOn("calendar.google", parts), false);
  assert.equal(featureOn("finance.bank", parts), false);
  assert.equal(featureOn("finance.ai-plan", parts), false);
  assert.equal(featureOn("food.ideas", parts), false);
  assert.equal(serviceOn("voice", parts), false);
  assert.equal(serviceOn("processor", parts), false);
  assert.equal(serviceOn("hostHealth", parts), false);
  for (const check of ["processing", "voice", "banking", "server"] as const)
    assert.equal(checkOn(check, parts), false, check);
  // The core's own parts stay.
  for (const check of [
    "inbox",
    "calendar",
    "notifications",
    "backups",
    "services",
  ] as const)
    assert.equal(checkOn(check, parts), true, check);
  assert.deepEqual(parts.nav, [
    "/",
    "/finance",
    "/tasks",
    "/inbox",
    "/calendar",
    "/food",
    "/activity",
    "/status",
  ]);
  assert.ok(serviceOn("tasks", parts));
  assert.ok(serviceOn("monitor", parts));
});

test("a module listed in LIFE_OS_MODULES brings its parts, and only its", () => {
  const parts = installedParts("voice");
  assert.deepEqual(parts.modules, ["core", "voice"]);
  assert.equal(featureOn("capture.voice", parts), true);
  assert.equal(checkOn("voice", parts), true);
  assert.equal(serviceOn("voice", parts), true);
  assert.equal(featureOn("calendar.google", parts), false);
  assert.equal(checkOn("processing", parts), false);
});

test("the list is Compose's: commas, spaces, and unknown names are fine", () => {
  assert.deepEqual(installedParts(" ai , cloudflare,,nonsense").modules, [
    "core",
    "ai",
    "cloudflare",
  ]);
});

test("unset, every module is on, as before modules", () => {
  assert.deepEqual(
    enabledModules(undefined).map((entry) => entry.id),
    MODULES.map((entry) => entry.id),
  );
  assert.deepEqual(installedParts(undefined), ALL_PARTS);
  for (const entry of optional)
    for (const feature of entry.dashboard.features)
      assert.ok(featureOn(feature, ALL_PARTS), feature);
});

test("every part belongs to one module", () => {
  for (const key of ["nav", "services", "checks", "features"] as const) {
    const all = ALL_PARTS[key] as string[];
    assert.equal(new Set(all).size, all.length, key);
  }
});

test("a call to a module that is off fails softly, as the gateway's", async () => {
  const response = moduleOffResponse();
  assert.equal(response.status, 404);
  assert.deepEqual((await response.json()).error.code, "module_off");
});
