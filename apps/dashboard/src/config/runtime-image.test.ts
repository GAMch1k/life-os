import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";

import nextConfig from "../../next.config";

const dashboardDir = path.join(__dirname, "..", "..");

// The Dockerfile's stages by name, in order; the last one is the runtime.
function dockerfileStages(): Map<string, string> {
  const dockerfile = readFileSync(
    path.join(dashboardDir, "Dockerfile"),
    "utf8",
  );
  const stages = new Map<string, string>();
  for (const stage of dockerfile.split(/^FROM /m).slice(1)) {
    const name = /^\S+ AS (\S+)$/m.exec(stage)?.[1];
    assert.ok(name, `an unnamed stage: FROM ${stage.split("\n")[0]}`);
    stages.set(name, stage);
  }
  return stages;
}

function runtimeStage(): string {
  return [...dockerfileStages().values()].at(-1) ?? "";
}

test("turns the Image Optimization API off", () => {
  assert.equal(nextConfig.images?.unoptimized, true);
});

test("ships next.config.ts in the runtime image, which next start reads", () => {
  const packageJson = JSON.parse(
    readFileSync(path.join(dashboardDir, "package.json"), "utf8"),
  ) as { scripts: Record<string, string> };

  // The image runs package.json's start script with node, not through npm.
  assert.equal(packageJson.scripts.start, "next start");
  assert.match(
    runtimeStage(),
    /^CMD \["node", "node_modules\/next\/dist\/bin\/next", "start"\]$/m,
  );
  assert.match(
    runtimeStage(),
    /^COPY --from=build \/app\/next\.config\.ts \.\/next\.config\.ts$/m,
  );
});

test("runs as node, with production dependencies only and without npm", () => {
  const runtime = runtimeStage();

  assert.match(
    dockerfileStages().get("production-dependencies") ?? "",
    /^RUN npm ci --omit=dev$/m,
  );
  assert.match(
    runtime,
    /^COPY --from=production-dependencies \/app\/node_modules \.\/node_modules$/m,
  );
  assert.doesNotMatch(
    runtime,
    /^COPY --from=(?!production-dependencies )\S+ \/app\/node_modules/m,
  );
  for (const tool of ["npm", "npx", "corepack"]) {
    assert.match(
      runtime,
      new RegExp(`/usr/local/bin/${tool}\\b`),
      `the runtime stage removes ${tool}`,
    );
  }
  // .next is the one directory the server writes to (its cache).
  assert.match(
    runtime,
    /^COPY --from=build --chown=node:node \/app\/\.next \.\/\.next$/m,
  );
  assert.match(runtime, /^USER node$/m);
});
