// Byte counts as the Status page shows them; safe for client components.

/** For example "520 MiB" or "7.7 GiB": powers of 1024, as free -h and df -h. */
export function formatBinaryBytes(bytes: number) {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return unit === 0
    ? `${value} B`
    : `${value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}
