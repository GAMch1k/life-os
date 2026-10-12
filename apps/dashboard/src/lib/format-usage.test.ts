import assert from "node:assert/strict";
import test from "node:test";

import { formatCores, formatUsage } from "./format-usage";

const GiB = 1024 ** 3;

test("formatCores counts CPUs busy, with hundredths only when that small", () => {
  assert.equal(formatCores(0), "0 cores");
  assert.equal(formatCores(0.004), "0 cores");
  assert.equal(formatCores(0.04), "0.04 cores");
  assert.equal(formatCores(0.4), "0.4 cores");
  assert.equal(formatCores(0.449), "0.4 cores");
  assert.equal(formatCores(1.75), "1.8 cores");
  assert.equal(formatCores(6), "6.0 cores");
});

test("formatUsage says CPU and RAM with their share of the server", () => {
  assert.equal(
    formatUsage({
      cpuCores: 0.4,
      cpuPercent: 6.7,
      memoryBytes: 2.1 * GiB,
      memoryPercent: 27.2,
    }),
    "CPU 0.4 cores (7%) · RAM 2.1 GiB (27%)",
  );
  // Right after a restart, CPU waits for a second sample.
  assert.equal(
    formatUsage({ memoryBytes: 600 * 1024 ** 2, memoryPercent: 7.6 }),
    "CPU not measured yet · RAM 600 MiB (8%)",
  );
});
