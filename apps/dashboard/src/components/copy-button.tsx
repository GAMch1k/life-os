"use client";

import { Check, Copy } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type Status = "idle" | "copied" | "failed";

const FEEDBACK_MS = { copied: 2000, failed: 6000 } as const;

/**
 * Plain-http fallback (the dashboard is also opened over WireGuard without
 * TLS), where the Clipboard API does not exist: copy from a hidden textarea.
 */
function copyWithSelection(text: string) {
  const focused =
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
  const area = document.createElement("textarea");
  area.value = text;
  area.readOnly = true;
  area.setAttribute("aria-hidden", "true");
  area.style.position = "fixed";
  area.style.inset = "0 auto auto 0";
  area.style.opacity = "0";
  document.body.append(area);
  area.focus();
  area.select();
  let copied = false;
  try {
    copied = document.execCommand("copy");
  } catch {
    copied = false;
  }
  area.remove();
  focused?.focus();
  return copied;
}

async function copyText(text: string) {
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Denied or unavailable: try the selection fallback.
    }
  }
  return copyWithSelection(text);
}

/**
 * An icon button that copies the text of the element with `targetId`, so what
 * is copied is exactly what is shown, or `text` when given (the same content,
 * laid out for pasting). It announces "Copied"; if copying is not possible it
 * selects the element's text instead, for the user to copy by hand.
 */
export function CopyButton({
  className,
  label,
  targetId,
  text,
}: {
  className?: string;
  label: string;
  targetId: string;
  text?: string;
}) {
  const [status, setStatus] = useState<Status>("idle");
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  async function copy() {
    const target = document.getElementById(targetId);
    if (!target) return;
    const copied = await copyText(text ?? target.textContent ?? "");
    if (!copied) window.getSelection()?.selectAllChildren(target);
    const next = copied ? "copied" : "failed";
    setStatus(next);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setStatus("idle"), FEEDBACK_MS[next]);
  }

  return (
    <span className={cn("flex min-w-0 items-center gap-1.5", className)}>
      <span
        aria-live="polite"
        className={cn(
          "min-w-0 text-right text-xs",
          status === "failed" ? "text-destructive" : "text-muted-foreground",
        )}
        role="status"
      >
        {status === "copied"
          ? "Copied"
          : status === "failed"
            ? "Copy failed. The text is selected; copy it by hand."
            : ""}
      </span>
      <Button
        aria-label={label}
        className="text-muted-foreground"
        onClick={copy}
        size="icon-sm"
        title={label}
        variant="ghost"
      >
        {status === "copied" ? (
          <Check aria-hidden="true" className="text-success size-4" />
        ) : (
          <Copy aria-hidden="true" className="size-4" />
        )}
      </Button>
    </span>
  );
}
