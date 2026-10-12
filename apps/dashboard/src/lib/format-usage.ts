// CPU and memory use as the Status page's Server card shows them; safe for
// client components.

import type { components } from "@/lib/api/generated/host-health";
import { formatBinaryBytes } from "@/lib/format-bytes";

/** A part of the server's use, as host-health reports it. */
export type UsageShare = components["schemas"]["HostUsageShare"];

/**
 * CPU in cores, where 1 is one CPU busy all the time: "0.4 cores",
 * "1.8 cores", or "0.04 cores" when that small.
 */
export function formatCores(cores: number) {
  if (cores < 0.005) return "0 cores";
  return `${cores < 0.1 ? cores.toFixed(2) : cores.toFixed(1)} cores`;
}

/**
 * A part of the server's use with its share of the server, such as
 * "CPU 0.4 cores (7%) · RAM 2.1 GiB (27%)". CPU needs two samples, so
 * right after a restart it is not measured yet.
 */
export function formatUsage(share: UsageShare) {
  const cpu =
    share.cpuCores === undefined
      ? "CPU not measured yet"
      : `CPU ${formatCores(share.cpuCores)} (${Math.round(share.cpuPercent ?? 0)}%)`;
  return `${cpu} · RAM ${formatBinaryBytes(share.memoryBytes)} (${Math.round(share.memoryPercent)}%)`;
}
