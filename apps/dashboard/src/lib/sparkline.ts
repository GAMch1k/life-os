/** One point of a sparkline: a time and a value. */
export type SparkPoint = { at: string; value: number };

/**
 * Reduces samples (oldest first) to at most `buckets` points over equal
 * spans of time, keeping each span's lowest sample (for memory available,
 * the worst moment is the one that matters) and ending with the latest
 * sample, so the last point is now.
 */
export function downsampleMin(
  samples: SparkPoint[],
  buckets: number,
): SparkPoint[] {
  if (samples.length <= buckets) return samples;
  const latest = samples[samples.length - 1];
  const earlier = samples.slice(0, -1);
  const spans = buckets - 1;
  const first = Date.parse(earlier[0].at);
  const span = Date.parse(latest.at) - first || 1;
  const lowest = new Map<number, SparkPoint>();
  for (const sample of earlier) {
    const bucket = Math.min(
      spans - 1,
      Math.floor(((Date.parse(sample.at) - first) / span) * spans),
    );
    const current = lowest.get(bucket);
    if (!current || sample.value < current.value) lowest.set(bucket, sample);
  }
  return [
    ...[...lowest.entries()]
      .sort(([a], [b]) => a - b)
      .map(([, point]) => point),
    latest,
  ];
}

/** The point with the lowest value, the earliest of equals. */
export function lowestPoint(points: SparkPoint[]) {
  return points.reduce<SparkPoint | undefined>(
    (low, point) => (!low || point.value < low.value ? point : low),
    undefined,
  );
}

/**
 * The SVG geometry of a sparkline in a width × height box, with values
 * from 0 (bottom) to max (top): its line and area paths and each point's
 * position.
 */
export function sparkGeometry(
  points: SparkPoint[],
  max: number,
  width: number,
  height: number,
) {
  const last = Math.max(1, points.length - 1);
  const xs = points.map((_, index) => (index / last) * width);
  const ys = points.map(
    (point) =>
      height - Math.min(1, Math.max(0, point.value / (max || 1))) * height,
  );
  const line = xs
    .map((x, index) => `${index ? "L" : "M"}${round(x)} ${round(ys[index])}`)
    .join(" ");
  const area = points.length
    ? `${line} L${round(xs[xs.length - 1])} ${height} L0 ${height} Z`
    : "";
  return { line, area, xs, ys };
}

function round(value: number) {
  return Math.round(value * 100) / 100;
}
