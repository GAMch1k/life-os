"use client";

import { useLayoutEffect, useRef } from "react";

/** Where the pane sticks: below the 3rem header and the page's 1.5rem padding. */
const STICKY_TOP_PX = 72;

/**
 * The selected entry beside the Event Log list. On wide screens it sticks
 * below the header and is never taller than the space left on screen, so a
 * tall entry scrolls on its own and its end stays reachable, also before the
 * page has scrolled far enough for the pane to stick. Phones keep the normal
 * page flow. Another entry opens at its top.
 */
export function SelectedEventPane({
  children,
  selectedId,
}: {
  children: React.ReactNode;
  selectedId?: string;
}) {
  const ref = useRef<HTMLElement>(null);

  useLayoutEffect(() => {
    const pane = ref.current;
    if (!pane) return;
    let frame = 0;
    const fit = () => {
      frame = 0;
      const top = Math.max(pane.getBoundingClientRect().top, STICKY_TOP_PX);
      pane.style.setProperty("--pane-top", `${top}px`);
    };
    const schedule = () => {
      frame ||= requestAnimationFrame(fit);
    };
    fit();
    window.addEventListener("scroll", schedule, { passive: true });
    window.addEventListener("resize", schedule);
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener("scroll", schedule);
      window.removeEventListener("resize", schedule);
    };
  }, []);

  useLayoutEffect(() => {
    ref.current?.scrollTo({ top: 0 });
  }, [selectedId]);

  return (
    <section
      aria-label="Selected event"
      // The 1px padding keeps the card's ring inside the scroll area.
      className="lg:sticky lg:top-18 lg:-m-px lg:max-h-[calc(100dvh_-_var(--pane-top,4.5rem)_-_1.5rem)] lg:overflow-y-auto lg:overscroll-contain lg:p-px"
      ref={ref}
    >
      {children}
    </section>
  );
}
