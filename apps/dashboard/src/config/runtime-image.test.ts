import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";

import nextConfig from "../../next.config";

const dashboardDir = path.join(__dirname, "..", "..");

test("turns the Image Optimization API off", () => {
  assert.equal(nextConfig.images?.unoptimized, true);
});

test("ships next.config.ts in the runtime image, which next start reads", () => {
  const dockerfile = readFileSync(
    path.join(dashboardDir, "Dockerfile"),
    "utf8",
  );
  const runtimeStage = dockerfile.split(/^FROM /m).at(-1) ?? "";
  const packageJson = JSON.parse(
    readFileSync(path.join(dashboardDir, "package.json"), "utf8"),
  ) as { scripts: Record<string, string> };

  assert.match(runtimeStage, /^CMD \["npm", "run", "start"\]$/m);
  assert.equal(packageJson.scripts.start, "next start");
  assert.match(
    runtimeStage,
    /^COPY --from=build \/app\/next\.config\.ts \.\/next\.config\.ts$/m,
  );
});
