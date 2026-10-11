"use client";

import { useEffect, useRef, useState, useTransition } from "react";
import { Check, Pencil, Trash2, TriangleAlert } from "lucide-react";

import {
  CaptureComposer,
  type ComposerCapture,
} from "@/components/capture-composer";
import { CaptureImageStrip } from "@/components/capture-images";
import { InboxSection, SectionUnavailable } from "@/components/inbox-summary";
import { Button } from "@/components/ui/button";
import { inboxClient } from "@/lib/api/inbox-client";
import { captureImages } from "@/lib/capture-images";
import {
  type HumanReviewItem,
  replacementMetadata,
  replacementNotice,
  reviewTotal,
} from "@/lib/human-review";
import { formatInboxTime, shownOfTotal } from "@/lib/inbox-summary";
import { cn } from "@/lib/utils";

type Candidate = { id?: string; title?: string; notes?: string };
type Review = { errorSummary?: string; candidates?: Candidate[] };

/** How long the queue says what became of an edit. */
const NOTICE_MS = 6_000;

function reviewFor(item: HumanReviewItem): Review {
  return (item.processingMetadata?.humanReview as Review | undefined) ?? {};
}

// Discarding keeps the capture in Inbox history but takes it out of review
// and processing; it uses the same Inbox operation as anywhere else.
async function requestDiscard(itemId: string) {
  try {
    const { data, error } = await inboxClient.POST(
      "/v1/inbox-items/{itemId}/discard",
      { params: { path: { itemId } } },
    );
    return !error && Boolean(data);
  } catch {
    return false;
  }
}

export function HumanReviewQueue({
  initialItems,
  renderedAt,
  timeZone,
  total: initialTotal,
}: {
  /** `null` when Inbox could not be read. */
  initialItems: HumanReviewItem[] | null;
  renderedAt: string;
  timeZone: string;
  /** The true number waiting, from the Inbox backlog. */
  total?: number;
}) {
  const [items, setItems] = useState(initialItems);
  const [total, setTotal] = useState(reviewTotal(initialItems, initialTotal));
  // A router.refresh(), after a capture on this page settled, renders the
  // page again with the current queue; it replaces what this one holds.
  const [loaded, setLoaded] = useState(initialItems);
  if (loaded !== initialItems) {
    setLoaded(initialItems);
    setItems(initialItems);
    setTotal(reviewTotal(initialItems, initialTotal));
  }
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [pending, startTransition] = useTransition();
  const now = new Date(renderedAt);

  useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(undefined), NOTICE_MS);
    return () => window.clearTimeout(timer);
  }, [notice]);

  function remove(itemId: string) {
    setItems(
      (current) => current?.filter((item) => item.id !== itemId) ?? null,
    );
    setTotal((current) => (current ? current - 1 : current));
  }
  function choose(itemId: string, candidateId: string) {
    setError(undefined);
    startTransition(async () => {
      const { data, error: requestError } = await inboxClient.POST(
        "/v1/inbox-items/{itemId}/human-review/resolve",
        { params: { path: { itemId } }, body: { candidateId } },
      );
      if (requestError || !data) {
        setError("Could not submit that review decision. Please try again.");
        return;
      }
      remove(itemId);
    });
  }
  function discard(itemId: string) {
    setError(undefined);
    startTransition(async () => {
      if (!(await requestDiscard(itemId))) {
        setError("Could not discard that capture. Please try again.");
        return;
      }
      remove(itemId);
    });
  }
  // An edit went through the normal capture path (text into Inbox, a
  // recording into Voice). Only then is the original discarded, so a failed
  // capture never loses it. False when the original stays in review.
  async function replace(itemId: string, capture: ComposerCapture) {
    setError(undefined);
    if (!(await requestDiscard(itemId))) return false;
    remove(itemId);
    setNotice(replacementNotice(capture.kind));
    return true;
  }
  // Stays while it says what became of an edit, even with nothing left.
  if (items && !items.length && !total && !notice) return null;
  return (
    <InboxSection
      count={total}
      icon={<TriangleAlert aria-hidden="true" className="size-4" />}
      id="human-review"
      note={items ? shownOfTotal(items.length, total) : undefined}
      title="Human Review"
    >
      {/* Always rendered, so screen readers hear the notice. Not the last
          child: under space-y it would leave a gap even while hidden. */}
      <p
        aria-live="polite"
        className={cn("text-muted-foreground", !notice && "sr-only")}
        role="status"
      >
        {notice}
      </p>
      {!items ? (
        <SectionUnavailable />
      ) : items.length ? (
        <div className="space-y-2">
          {items.map((item) => (
            <ReviewCard
              item={item}
              key={item.id}
              now={now}
              onChoose={(candidateId) => choose(item.id, candidateId)}
              onDiscard={() => discard(item.id)}
              onReplace={(capture) => replace(item.id, capture)}
              pending={pending}
              timeZone={timeZone}
            />
          ))}
        </div>
      ) : null}
      {error ? <p className="text-destructive text-sm">{error}</p> : null}
    </InboxSection>
  );
}

/**
 * One capture waiting for a decision: its candidate Tasks, Edit, and Discard.
 * Edit turns the card into the capture composer, prefilled with the capture's
 * text (a voice capture's transcript). Capturing there sends the edit as a
 * new capture that names this one (`sourceMetadata.replacesItemId`), then
 * discards this one; Cancel leaves it as it was.
 */
function ReviewCard({
  item,
  now,
  onChoose,
  onDiscard,
  onReplace,
  pending,
  timeZone,
}: {
  item: HumanReviewItem;
  now: Date;
  onChoose: (candidateId: string) => void;
  onDiscard: () => void;
  /** Discards this capture once its edit was captured; false if it failed. */
  onReplace: (capture: ComposerCapture) => Promise<boolean>;
  pending: boolean;
  timeZone: string;
}) {
  const [editing, setEditing] = useState(false);
  const [problem, setProblem] = useState<string>();
  const editButton = useRef<HTMLButtonElement>(null);
  const discardButton = useRef<HTMLButtonElement>(null);
  // Where focus goes once the editor closes: back to Edit after Cancel, to
  // Discard when the original could not be discarded.
  const focusAfterEdit = useRef<"edit" | "discard" | undefined>(undefined);
  const review = reviewFor(item);
  const photos = captureImages(item);
  const candidates = (review.candidates ?? []).filter(
    (candidate) => candidate.id,
  );

  useEffect(() => {
    if (editing || !focusAfterEdit.current) return;
    const button =
      focusAfterEdit.current === "edit" ? editButton : discardButton;
    focusAfterEdit.current = undefined;
    button.current?.focus();
  }, [editing]);

  function edit() {
    setProblem(undefined);
    setEditing(true);
  }
  function cancel() {
    focusAfterEdit.current = "edit";
    setEditing(false);
  }
  async function captured(capture: ComposerCapture) {
    setProblem(undefined);
    // Once the original is discarded, the queue removes this card.
    if (await onReplace(capture)) return;
    focusAfterEdit.current = "discard";
    setEditing(false);
    setProblem(
      "Your edit was captured, but the original could not be discarded. Discard it to finish.",
    );
  }

  return (
    <section className="rounded-lg border p-3">
      <div className="flex items-start gap-3">
        <p className="min-w-0 flex-1 font-medium wrap-anywhere whitespace-pre-line">
          {item.content}
        </p>
        <time
          className="text-muted-foreground shrink-0 text-xs tabular-nums"
          dateTime={item.createdAt}
        >
          {formatInboxTime(item.createdAt, timeZone, now)}
        </time>
      </div>
      {editing ? null : (
        <CaptureImageStrip label={item.content} references={photos} />
      )}
      {editing ? (
        <>
          <p className="text-muted-foreground mt-1">
            Edit the text or record it again. Capture sends it to Inbox as a new
            capture and discards this one; Cancel keeps this one here.
          </p>
          <div className="mt-2">
            <CaptureComposer
              focus="always"
              id={`review-edit-${item.id}`}
              initialContent={item.content}
              initialMedia={photos}
              label="Edited capture"
              metadata={replacementMetadata(item.id)}
              onCancel={cancel}
              onCaptured={captured}
              placeholder="Edit the capture…"
            />
          </div>
        </>
      ) : (
        <>
          <p className="text-muted-foreground mt-1">
            {review.errorSummary ?? "Choose the intended Task."}
          </p>
          {candidates.length === 0 ? (
            <p className="text-muted-foreground mt-1">
              Life OS found nothing it can safely do with this capture. Discard
              it, or edit it to add more detail.
            </p>
          ) : null}
          <div className="mt-2 flex flex-wrap gap-2">
            {candidates.map((candidate) => (
              <Button
                className="h-auto min-h-8 max-w-full py-1 text-left whitespace-normal"
                disabled={pending}
                key={candidate.id}
                onClick={() => onChoose(candidate.id!)}
                variant="outline"
              >
                <Check /> {candidate.title ?? candidate.id}
              </Button>
            ))}
            <Button
              aria-label={`Edit capture: ${item.content}`}
              disabled={pending}
              onClick={edit}
              ref={editButton}
              variant="ghost"
            >
              <Pencil /> Edit
            </Button>
            <Button
              aria-label={`Discard capture: ${item.content}`}
              disabled={pending}
              onClick={onDiscard}
              ref={discardButton}
              variant="ghost"
            >
              <Trash2 /> Discard
            </Button>
          </div>
        </>
      )}
      {problem ? (
        <p className="text-destructive mt-2 text-sm">{problem}</p>
      ) : null}
    </section>
  );
}
