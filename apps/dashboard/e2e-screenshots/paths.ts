import path from "node:path";

/**
 * The demo account's browser state, which the demo project saves and the
 * shots project signs in with. It holds a session cookie: it stays in the
 * run's outputDir and is never uploaded.
 */
export const STATE = path.join(
  __dirname,
  "..",
  "test-results-screenshots",
  "demo-session.json",
);

/** Where the PNG files go (tools/screenshots sets it). */
export function screenshotsDir() {
  const dir = process.env.LIFE_OS_SCREENSHOTS_DIR;
  if (!dir) throw new Error("set LIFE_OS_SCREENSHOTS_DIR (tools/screenshots)");
  return dir;
}
