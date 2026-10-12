"use client";

import { useId, useRef, useState } from "react";

import { formatBinaryBytes } from "@/lib/format-bytes";
import { formatCores } from "@/lib/format-usage";
import type { StatusChart } from "@/lib/runtime-status";
import { highestPoint, lowestPoint, sparkGeometry } from "@/lib/sparkline";
import { dateTimeFormat } from "@/lib/time-zone";

const WIDTH = 300;
const HEIGHT = 48;

/**
 * A card's small trend, such as memory available over a day: a 2px line
 * over a faint wash, a hairline at the warning level, and a dot at now. The
 * caption says the worst (the lowest, or for use the highest) and the
 * current value, so nothing depends on hovering; pointer and arrow keys
 * move a crosshair that shows each point.
 */
export function Sparkline({
  chart,
  timeZone,
}: {
  chart: StatusChart;
  timeZone: string;
}) {
  const [active, setActive] = useState<number | null>(null);
  const box = useRef<HTMLDivElement>(null);
  const captionId = useId();
  const { points, max, threshold } = chart;
  const { line, area, xs, ys } = sparkGeometry(points, max, WIDTH, HEIGHT);
  const format = chart.unit === "cores" ? formatCores : formatBinaryBytes;
  const highest = chart.worst === "highest";
  const worst = highest ? highestPoint(points) : lowestPoint(points);
  const last = points.length - 1;
  const time = dateTimeFormat(timeZone, { hour: "2-digit", minute: "2-digit" });
  const caption = `${chart.label}: ${highest ? "highest" : "lowest"} ${format(worst?.value ?? 0)} at ${worst ? time.format(new Date(worst.at)) : "–"}, now ${format(points[last].value)}${threshold === undefined ? "" : `; warning below ${format(threshold)}`}.`;

  function pointAt(clientX: number) {
    const rect = box.current?.getBoundingClientRect();
    if (!rect || rect.width === 0) return null;
    const ratio = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
    return Math.round(ratio * last);
  }

  function onKeyDown(event: React.KeyboardEvent) {
    if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
      event.preventDefault();
      const step = event.key === "ArrowLeft" ? -1 : 1;
      setActive((current) =>
        Math.min(last, Math.max(0, (current ?? last) + step)),
      );
    } else if (event.key === "Escape") {
      setActive(null);
    }
  }

  const left = (index: number) => `${(xs[index] / WIDTH) * 100}%`;
  const top = (index: number) => `${(ys[index] / HEIGHT) * 100}%`;
  return (
    <figure className="space-y-1.5">
      <div
        aria-describedby={captionId}
        aria-label={chart.label}
        className="focus-visible:ring-ring/50 relative h-12 rounded-sm outline-none focus-visible:ring-3"
        onBlur={() => setActive(null)}
        onFocus={() => setActive(last)}
        onKeyDown={onKeyDown}
        onPointerLeave={() => setActive(null)}
        onPointerMove={(event) => setActive(pointAt(event.clientX))}
        ref={box}
        role="img"
        tabIndex={0}
      >
        <svg
          aria-hidden="true"
          className="absolute inset-0 size-full overflow-visible"
          preserveAspectRatio="none"
          viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        >
          {threshold !== undefined && max > 0 && (
            <line
              className="stroke-border"
              strokeWidth={1}
              vectorEffect="non-scaling-stroke"
              x1={0}
              x2={WIDTH}
              y1={HEIGHT - (threshold / max) * HEIGHT}
              y2={HEIGHT - (threshold / max) * HEIGHT}
            />
          )}
          <path className="fill-chart-2 opacity-10" d={area} />
          <path
            className="stroke-chart-2"
            d={line}
            fill="none"
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            vectorEffect="non-scaling-stroke"
          />
        </svg>
        {/* The dot at now, outside the stretched SVG so it stays round. */}
        <span
          aria-hidden="true"
          className="bg-chart-2 ring-card absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2"
          style={{ left: left(last), top: top(last) }}
        />
        {active !== null && (
          <>
            <span
              aria-hidden="true"
              className="bg-muted-foreground/40 absolute inset-y-0 w-px"
              style={{ left: left(active) }}
            />
            <span
              aria-hidden="true"
              className="bg-chart-2 ring-card absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2"
              style={{ left: left(active), top: top(active) }}
            />
            <span
              className="bg-popover text-popover-foreground pointer-events-none absolute -top-1 z-10 -translate-y-full rounded-md border px-2 py-1 text-xs whitespace-nowrap shadow-sm"
              role="tooltip"
              style={
                xs[active] > WIDTH / 2
                  ? { right: `${100 - (xs[active] / WIDTH) * 100}%` }
                  : { left: left(active) }
              }
            >
              <span className="font-medium">
                {format(points[active].value)}
              </span>{" "}
              <span className="text-muted-foreground">
                {time.format(new Date(points[active].at))}
              </span>
            </span>
          </>
        )}
      </div>
      <figcaption className="text-muted-foreground text-xs" id={captionId}>
        {caption}
      </figcaption>
    </figure>
  );
}
