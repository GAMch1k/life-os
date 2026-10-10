"use client";

import { useId, useState } from "react";

import { cn } from "@/lib/utils";

/**
 * Short text with a "Show more" toggle that reveals its full version. Without
 * a longer version there is nothing to toggle, so only the text is shown.
 */
export function ExpandableText({
  className,
  fullText,
  text,
}: {
  className?: string;
  fullText?: string;
  text: string;
}) {
  const [expanded, setExpanded] = useState(false);
  const id = useId();
  if (!fullText || fullText === text) {
    return <p className={cn("wrap-anywhere", className)}>{text}</p>;
  }
  return (
    <p className={cn("wrap-anywhere", className)}>
      <span className={cn(expanded && "whitespace-pre-wrap")} id={id}>
        {expanded ? fullText : text}
      </span>{" "}
      <button
        aria-controls={id}
        aria-expanded={expanded}
        className="text-primary focus-visible:ring-ring/50 rounded-sm font-medium whitespace-nowrap underline-offset-4 outline-none hover:underline focus-visible:ring-3"
        onClick={() => setExpanded((value) => !value)}
        type="button"
      >
        {expanded ? "Show less" : "Show more"}
      </button>
    </p>
  );
}
