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
  return downsample(samples, buckets, (a, b) => a.value < b.value);
}

/**
 * As downsampleMin, keeping each span's highest sample instead: for use,
 * such as Life OS's memory, the peak is the moment that matters.
 */
export function downsampleMax(
  samples: SparkPoint[],
  buckets: number,
): SparkPoint[] {
  return downsample(samples, buckets, (a, b) => a.value > b.value);
}

function downsample(
  samples: SparkPoint[],
  buckets: number,
  worse: (a: SparkPoint, b: SparkPoint) => boolean,
): SparkPoint[] {
  if (samples.length <= buckets) return samples;
  const latest = samples[samples.length - 1];
  const earlier = samples.slice(0, -1);
  const spans = buckets - 1;
  const first = Date.parse(earlier[0].at);
  const span = Date.parse(latest.at) - first || 1;
  const kept = new Map<number, SparkPoint>();
  for (const sample of earlier) {
    const bucket = Math.min(
      spans - 1,
      Math.floor(((Date.parse(sample.at) - first) / span) * spans),
    );
    const current = kept.get(bucket);
    if (!current || worse(sample, current)) kept.set(bucket, sample);
  }
  return [
    ...[...kept.entries()].sort(([a], [b]) => a - b).map(([, point]) => point),
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

/** The point with the highest value, the earliest of equals. */
export function highestPoint(points: SparkPoint[]) {
  return points.reduce<SparkPoint | undefined>(
    (high, point) => (!high || point.value > high.value ? point : high),
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
