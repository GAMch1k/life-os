"use client";

import { Camera, ImagePlus, LoaderCircle, Send, X } from "lucide-react";
import {
  type ChangeEvent,
  type FormEvent,
  type KeyboardEvent,
  useEffect,
  useRef,
  useState,
} from "react";

import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { useFeature } from "@/components/modules-provider";
import { VoiceCapture } from "@/components/voice-capture";
import { inboxClient } from "@/lib/api/inbox-client";
import {
  IMAGE_ACCEPT,
  imageErrorMessage,
  mediaUrl,
  photosContent,
  photosProblem,
  uploadImages,
} from "@/lib/capture-images";
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

/**
 * A photo to capture: one already in Inbox (an edited capture's, by its
 * reference) or one picked here, shown from a local object URL until it is
 * uploaded with the capture.
 */
type Photo =
  | { key: string; reference: string; url: string }
  | { key: string; file: File; url: string };

/** The new Inbox item, or undefined when Inbox did not take the capture. */
async function createCapture(
  content: string,
  metadata: Record<string, unknown> | undefined,
  mediaReferences: string[],
) {
  try {
    // The browser's timezone is what "tomorrow at 10" means to the owner.
    const { data, error } = await inboxClient.POST("/v1/inbox-items", {
      body: {
        content,
        source: "manual",
        sourceMetadata: captureMetadata(metadata),
        ...(mediaReferences.length ? { mediaReferences } : {}),
      },
    });
    return error ? undefined : data;
  } catch {
    return undefined;
  }
}

/**
 * The capture composer: a chat-like text field where Enter captures and
 * Shift+Enter starts a new line, photo buttons (the gallery, and on a phone
 * the camera), the Voice recorder beside the Capture button, and a status
 * line. Text and photos go to Inbox (the photos to `POST /v1/media` first,
 * one at a time, then `POST /v1/inbox-items` naming them), a recording to
 * Voice, which transcribes it into an Inbox item; both send the browser's
 * timezone plus `metadata` as `sourceMetadata`. Every text capture is
 * announced to the page (`announceCapture`); `onCaptured` hears about both
 * kinds. With `onCancel` it also offers Cancel (and Escape), for editing a
 * capture in place; `initialMedia` are the edited capture's photos, kept
 * unless removed.
 */
export function CaptureComposer({
  focus,
  id,
  initialContent = "",
  initialMedia,
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
  /** Photos already in Inbox to capture again, by reference. */
  initialMedia?: string[];
  /** The text field's accessible name. */
  label: string;
  metadata?: Record<string, unknown>;
  onCancel?: () => void;
  onCaptured?: (capture: ComposerCapture) => void | Promise<void>;
  placeholder: string;
}) {
  // The recorder, while the voice notes module is on.
  const voice = useFeature("capture.voice");
  const [content, setContent] = useState(initialContent);
  const [photos, setPhotos] = useState<Photo[]>(() =>
    (initialMedia ?? []).flatMap((reference) => {
      const url = mediaUrl(reference);
      return url ? [{ key: reference, reference, url }] : [];
    }),
  );
  const [sending, setSending] = useState(false);
  const [notice, setNotice] = useState<{
    kind: "ok" | "error";
    text: string;
  }>();
  const field = useRef<HTMLTextAreaElement>(null);
  const gallery = useRef<HTMLInputElement>(null);
  const camera = useRef<HTMLInputElement>(null);
  // Guards against a second submit before React re-renders with `sending`.
  const submitting = useRef(false);
  // The photos' object URLs are released when the composer goes away.
  const shownPhotos = useRef(photos);
  useEffect(() => {
    shownPhotos.current = photos;
  }, [photos]);
  useEffect(
    () => () => {
      for (const photo of shownPhotos.current) {
        if ("file" in photo) URL.revokeObjectURL(photo.url);
      }
    },
    [],
  );

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

  function addPhotos(event: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(event.target.files ?? []);
    // The same photo can be picked again after removing it.
    event.target.value = "";
    if (!files.length) return;
    const problem = photosProblem(files, photos.length);
    if (problem) {
      setNotice({ kind: "error", text: imageErrorMessage(problem) });
      return;
    }
    setNotice(undefined);
    setPhotos((current) => [
      ...current,
      ...files.map((file) => ({
        key: `${file.name}-${file.size}-${file.lastModified}-${Math.random()}`,
        file,
        url: URL.createObjectURL(file),
      })),
    ]);
  }

  function removePhoto(key: string) {
    setPhotos((current) =>
      current.filter((photo) => {
        if (photo.key !== key) return true;
        if ("file" in photo) URL.revokeObjectURL(photo.url);
        return false;
      }),
    );
  }

  /** The photos' references, uploading the new ones; or why not. */
  async function photoReferences(): Promise<string[] | string> {
    const files = photos.flatMap((photo) =>
      "file" in photo ? [photo.file] : [],
    );
    const uploaded = files.length
      ? await uploadImages(files)
      : { references: [] };
    if (!uploaded.references) return imageErrorMessage(uploaded.code);
    let next = 0;
    return photos.map((photo) =>
      "file" in photo ? uploaded.references[next++] : photo.reference,
    );
  }

  async function submit(fromKeyboard: boolean) {
    if (submitting.current) return;
    const text = content.trim();
    if (!text && !photos.length) {
      if (!fromKeyboard) {
        setNotice({
          kind: "error",
          text: "Write something or add a photo to capture first.",
        });
      }
      return;
    }
    submitting.current = true;
    setSending(true);
    setNotice(undefined);
    // Clear right away like a chat composer; anything typed meanwhile stays.
    setContent("");
    const restore = () =>
      setContent((current) =>
        text ? (current ? `${text}\n${current}` : text) : current,
      );
    try {
      const references = await photoReferences();
      if (typeof references === "string") {
        restore();
        setNotice({ kind: "error", text: references });
        return;
      }
      const item = await createCapture(
        text || photosContent(references.length),
        metadata,
        references,
      );
      if (!item) {
        restore();
        setNotice({
          kind: "error",
          text: "Could not save that capture. Please try again.",
        });
        return;
      }
      for (const photo of photos) {
        if ("file" in photo) URL.revokeObjectURL(photo.url);
      }
      setPhotos([]);
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
      {photos.length ? (
        <ul aria-label="Photos to capture" className="flex flex-wrap gap-2">
          {photos.map((photo, index) => (
            <li className="relative" key={photo.key}>
              {/* A plain img: an object URL, or Inbox's image under the
                  owner's Access login. */}
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img
                alt={`Photo ${index + 1}`}
                className="bg-muted size-16 rounded-md border object-cover"
                src={photo.url}
              />
              <Button
                aria-label={`Remove photo ${index + 1}`}
                className="absolute -top-2 -right-2 rounded-full"
                disabled={sending}
                onClick={() => removePhoto(photo.key)}
                size="icon-xs"
                type="button"
                variant="secondary"
              >
                <X />
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
      {/* The gallery (and files); on a phone the camera has its own button,
          which opens it directly. */}
      <input
        accept={IMAGE_ACCEPT}
        className="hidden"
        multiple
        onChange={addPhotos}
        ref={gallery}
        tabIndex={-1}
        type="file"
      />
      <input
        accept={IMAGE_ACCEPT}
        capture="environment"
        className="hidden"
        onChange={addPhotos}
        ref={camera}
        tabIndex={-1}
        type="file"
      />
      <div className="flex items-start gap-2">
        <Button
          aria-label="Add photos"
          disabled={sending}
          onClick={() => gallery.current?.click()}
          size="icon-lg"
          title="Add photos"
          type="button"
          variant="outline"
        >
          <ImagePlus />
        </Button>
        <Button
          aria-label="Take a photo"
          className="pointer-fine:hidden"
          disabled={sending}
          onClick={() => camera.current?.click()}
          size="icon-lg"
          title="Take a photo"
          type="button"
          variant="outline"
        >
          <Camera />
        </Button>
        {voice ? (
          <VoiceCapture
            dropOnLeave={Boolean(onCancel)}
            metadata={metadata}
            onUploaded={
              onCaptured
                ? (job) => void onCaptured({ kind: "voice", job })
                : undefined
            }
          />
        ) : null}
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
