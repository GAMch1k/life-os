"use client";

import { useLayoutEffect, useRef } from "react";

import { packColumns } from "@/lib/masonry";
import { cn } from "@/lib/utils";

/** Firefox ignores grid lines past 10,000; a longer grid uses taller rows. */
const MAX_ROWS = 5_000;

/** Puts every card back in the grid's normal flow and returns the cards. */
function unpack(grid: HTMLElement) {
  delete grid.dataset.packed;
  const cards = Array.from(grid.children, (card) => card as HTMLElement);
  for (const { style } of cards) {
    style.gridColumn = "";
    style.gridRow = "";
    style.minHeight = "";
  }
  return cards;
}

/**
 * Measures the cards at their natural height in the grid's columns and places
 * each one with `packColumns`: rows are `row` pixels tall without a row gap,
 * so a card spans as many rows as it is tall and the gap is left as empty
 * rows. A column's last card grows to the grid's last row. Measuring and
 * placing happen in one go, so the unpacked grid is never painted.
 */
function pack(grid: HTMLElement) {
  const cards = unpack(grid);
  const style = getComputedStyle(grid);
  const columns = style.gridTemplateColumns
    .split(" ")
    .filter((track) => track.endsWith("px")).length;
  if (columns < 2 || cards.length === 0) return;
  const gap = parseFloat(style.columnGap) || 0;
  const heights = cards.map((card) => card.getBoundingClientRect().height);
  const stacked = heights.reduce((sum, height) => sum + height + gap, 0);
  const row = Math.max(1, Math.ceil(stacked / MAX_ROWS));
  const layout = packColumns(
    heights.map((height) => Math.ceil(height / row)),
    columns,
    Math.round(gap / row),
  );
  grid.style.setProperty("--masonry-row", `${row}px`);
  grid.dataset.packed = "";
  layout.placements.forEach((placement, index) => {
    const { style } = cards[index];
    style.gridColumn = String(placement.column + 1);
    style.gridRow = `${placement.top + 1} / span ${placement.span}`;
    if (placement.last) style.minHeight = `${placement.span * row}px`;
  });
}

/**
 * A grid whose cards, from two columns up, sit tightly under each other
 * instead of in rows as tall as their tallest card: each card goes under the
 * shortest column, in order, and only each column's last card stretches so
 * the columns end level. The cards stay in their order in the page, so
 * screen readers and Tab follow it. The server renders a plain grid with
 * cards at their natural height; packing starts right after hydration and
 * follows any change in a card's size, such as new content or a resize
 * across the breakpoint. One column keeps the plain grid.
 */
export function MasonryGrid({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const grid = ref.current;
    if (!grid) return;
    let frame = 0;
    const schedule = () => {
      frame ||= requestAnimationFrame(() => {
        frame = 0;
        pack(grid);
      });
    };
    // A stretched card keeps its height when its content shrinks, so its
    // parts are watched too. Packing waits for the next frame: changing
    // watched sizes inside the observer's callback would be reported as a
    // loop. A window resize is followed in the frame it happens in.
    const observer = new ResizeObserver(schedule);
    for (const card of grid.children) {
      observer.observe(card);
      for (const part of card.children) observer.observe(part);
    }
    window.addEventListener("resize", schedule);
    pack(grid);
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
      window.removeEventListener("resize", schedule);
    };
  }, [children]);

  return (
    <div
      className={cn(
        "grid items-start gap-4 data-packed:auto-rows-(--masonry-row) data-packed:gap-y-0",
        className,
      )}
      ref={ref}
    >
      {children}
    </div>
  );
}
