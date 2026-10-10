"use client";

import { LoaderCircle, Send } from "lucide-react";
import {
  type FormEvent,
  type KeyboardEvent,
  useEffect,
  useRef,
  useState,
} from "react";

import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { VoiceCapture } from "@/components/voice-capture";
import { inboxClient } from "@/lib/api/inbox-client";
import { announceCapture } from "@/lib/capture-session";
import type { InboxItem } from "@/lib/inbox-summary";
import { captureMetadata } from "@/lib/time-zone";
import { cn } from "@/lib/utils";
import type { VoiceJob } from "@/lib/voice-job-state";

const CONFIRMATION_MS = 2_500;

/**
 * What the composer captured: a new Inbox item, or a recording Voice
 * accepted, which becomes an Inbox item once it is transcribed.
 */
export type ComposerCapture =
  { kind: "text"; item: InboxItem } | { kind: "voice"; job: VoiceJob };

/** The new Inbox item, or undefined when Inbox did not take the capture. */
async function createCapture(
  content: string,
  metadata: Record<string, unknown> | undefined,
) {
  try {
    // The browser's timezone is what "tomorrow at 10" means to the owner.
    const { data, error } = await inboxClient.POST("/v1/inbox-items", {
      body: {
        content,
        source: "manual",
        sourceMetadata: captureMetadata(metadata),
      },
    });
    return error ? undefined : data;
  } catch {
    return undefined;
  }
}

/**
 * The capture composer: a chat-like text field where Enter captures and
 * Shift+Enter starts a new line, the Voice recorder beside the Capture
 * button, and a status line. Text goes to Inbox (`POST /v1/inbox-items`), a
 * recording to Voice, which transcribes it into an Inbox item; both send the
 * browser's timezone plus `metadata` as `sourceMetadata`. Every text capture
 * is announced to the page (`announceCapture`); `onCaptured` hears about both
 * kinds. With `onCancel` it also offers Cancel (and Escape), for editing a
 * capture in place.
 */
export function CaptureComposer({
  focus,
  id,
  initialContent = "",
  label,
  metadata,
  onCancel,
  onCaptured,
  placeholder,
}: {
  /** Focus the field when shown: on desktop only, or always. */
  focus?: "desktop" | "always";
  /** The text field's id; its hint is `${id}-hint`. */
  id: string;
  initialContent?: string;
  /** The text field's accessible name. */
  label: string;
  metadata?: Record<string, unknown>;
  onCancel?: () => void;
  onCaptured?: (capture: ComposerCapture) => void | Promise<void>;
  placeholder: string;
}) {
  const [content, setContent] = useState(initialContent);
  const [sending, setSending] = useState(false);
  const [notice, setNotice] = useState<{
    kind: "ok" | "error";
    text: string;
  }>();
  const field = useRef<HTMLTextAreaElement>(null);
  // Guards against a second submit before React re-renders with `sending`.
  const submitting = useRef(false);

  useEffect(() => {
    const element = field.current;
    if (!element || !focus) return;
    // Desktop opens ready to type; phones keep the keyboard closed until
    // tapped, unless the owner asked for the field (Edit).
    if (focus === "desktop" && !window.matchMedia("(pointer: fine)").matches) {
      return;
    }
    element.focus({ preventScroll: true });
    // Continue at the end of a prefilled text.
    element.setSelectionRange(element.value.length, element.value.length);
  }, [focus]);

  useEffect(() => {
    if (notice?.kind !== "ok") return;
    const timer = window.setTimeout(
      () => setNotice(undefined),
      CONFIRMATION_MS,
    );
    return () => window.clearTimeout(timer);
  }, [notice]);

  async function submit(fromKeyboard: boolean) {
    if (submitting.current) return;
    const text = content.trim();
    if (!text) {
      if (!fromKeyboard) {
        setNotice({ kind: "error", text: "Write something to capture first." });
      }
      return;
    }
    submitting.current = true;
    setSending(true);
    setNotice(undefined);
    // Clear right away like a chat composer; anything typed meanwhile stays.
    setContent("");
    try {
      const item = await createCapture(text, metadata);
      if (!item) {
        setContent((current) => (current ? `${text}\n${current}` : text));
        setNotice({
          kind: "error",
          text: "Could not save that capture. Please try again.",
        });
        return;
      }
      setNotice({ kind: "ok", text: "Captured." });
      announceCapture(item);
      await onCaptured?.({ kind: "text", item });
    } finally {
      submitting.current = false;
      setSending(false);
      // Keep typing after a capture, also after clicking Capture (the button
      // was disabled meanwhile, which may have dropped focus to the body).
      const active = document.activeElement;
      if (
        !active ||
        active === document.body ||
        active === field.current ||
        (active instanceof HTMLButtonElement && active.type === "submit")
      ) {
        field.current?.focus({ preventScroll: true });
      }
    }
  }

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void submit(false);
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    // Enter also confirms IME composition (for example Japanese input), and
    // Escape cancels it.
    if (event.nativeEvent.isComposing || event.keyCode === 229) return;
    if (event.key === "Escape" && onCancel && !sending) {
      event.preventDefault();
      onCancel();
      return;
    }
    if (event.key !== "Enter" || event.shiftKey || event.altKey) return;
    event.preventDefault();
    if (!event.repeat) void submit(true);
  }

  return (
    // A flex column, not space-y: the hidden status line is the last child,
    // and space-y left a margin under the buttons for it (BUG-30). An sr-only
    // element takes no part in flex layout.
    <form className="flex flex-col gap-2" onSubmit={onSubmit}>
      <label className="sr-only" htmlFor={id}>
        {label}
      </label>
      <Textarea
        aria-describedby={`${id}-hint`}
        className="max-h-64 min-h-16 resize-none sm:min-h-20"
        enterKeyHint="send"
        id={id}
        onChange={(event) => setContent(event.target.value)}
        onKeyDown={onKeyDown}
        placeholder={placeholder}
        ref={field}
        value={content}
      />
      <div className="flex items-start gap-2">
        <VoiceCapture
          metadata={metadata}
          onUploaded={
            onCaptured
              ? (job) => void onCaptured({ kind: "voice", job })
              : undefined
          }
        />
        <span
          className="text-muted-foreground hidden shrink-0 self-center text-xs lg:inline"
          id={`${id}-hint`}
        >
          Enter to capture · Shift+Enter for a new line
          {onCancel ? " · Esc to cancel" : null}
        </span>
        {onCancel ? (
          <Button
            disabled={sending}
            onClick={onCancel}
            size="lg"
            type="button"
            variant="ghost"
          >
            Cancel
          </Button>
        ) : null}
        <Button disabled={sending} size="lg" type="submit">
          {sending ? (
            <LoaderCircle aria-hidden="true" className="animate-spin" />
          ) : (
            <Send aria-hidden="true" />
          )}
          Capture
        </Button>
      </div>
      <p
        aria-live="polite"
        className={cn(
          "text-xs",
          notice?.kind === "error"
            ? "text-destructive"
            : "text-muted-foreground",
          !notice && "sr-only",
        )}
        role="status"
      >
        {notice?.text}
      </p>
    </form>
  );
}
