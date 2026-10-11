"use client";

import { Link2 } from "lucide-react";
import { type FormEvent, useId, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { recipeImportClient } from "@/lib/api/recipe-import-client";
import { load } from "@/lib/food";
import {
  type ImportDraft,
  type ImportExtras,
  type ImportPreview,
  isSocialLink,
  previewSummary,
  withCaption,
} from "@/lib/food-import";
import { cn } from "@/lib/utils";

/**
 * Imports a recipe from a link: paste it, see what the page gives (the
 * whole recipe, or for Instagram, TikTok, and pages without recipe data
 * only a preview, whose caption can be pasted to fill in the ingredients
 * and steps), then review and save it in the recipe form.
 */
export function ImportDialog({
  open,
  onClose,
  onContinue,
}: {
  open: boolean;
  onClose: () => void;
  /** Review the draft in the recipe form, then save it. */
  onContinue: (draft: ImportDraft, extras: ImportExtras) => void;
}) {
  return (
    <Dialog
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      open={open}
    >
      <DialogContent className="sm:max-w-lg">
        {open ? (
          <ImportForm onCancel={onClose} onContinue={onContinue} />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function ImportForm({
  onCancel,
  onContinue,
}: {
  onCancel: () => void;
  onContinue: (draft: ImportDraft, extras: ImportExtras) => void;
}) {
  const id = useId();
  const [url, setUrl] = useState("");
  const [preview, setPreview] = useState<ImportPreview>();
  const [caption, setCaption] = useState("");
  const [captionNote, setCaptionNote] = useState<string>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();
  const link = preview?.draft.sourceUrl ?? url.trim();
  const social = isSocialLink(link);

  const read = (event: FormEvent) => {
    event.preventDefault();
    if (!url.trim()) return;
    setPending(true);
    setError(undefined);
    setCaptionNote(undefined);
    void load(
      () =>
        recipeImportClient.POST("/v1/imports/preview", {
          body: { url: url.trim() },
        }),
      "The link could not be read.",
    ).then((result) => {
      setPending(false);
      if (result.error !== undefined) return setError(result.error);
      setPreview(result.data);
      // A social post's caption often comes with the preview: start there.
      setCaption(
        result.data.method !== "structured" && isSocialLink(url.trim())
          ? (result.data.draft.description ?? "")
          : "",
      );
    });
  };

  const readCaption = () => {
    if (!preview || !caption.trim()) return;
    setPending(true);
    setError(undefined);
    void load(
      () =>
        recipeImportClient.POST("/v1/imports/text", {
          body: { text: caption, sourceUrl: link || undefined },
        }),
      "The caption could not be read.",
    ).then((result) => {
      setPending(false);
      if (result.error !== undefined) return setError(result.error);
      const read = result.data.draft;
      setPreview({
        ...preview,
        draft: withCaption(preview.draft, read, social),
      });
      setCaptionNote(
        result.data.reason ??
          `Read ${read.ingredients.length} ingredient${read.ingredients.length === 1 ? "" : "s"} and ${read.steps.length} step${read.steps.length === 1 ? "" : "s"}.`,
      );
    });
  };

  const draft = preview?.draft;

  return (
    <div className="grid gap-4">
      <DialogHeader>
        <DialogTitle>Import from link</DialogTitle>
        <DialogDescription>
          Paste a recipe link: a recipe site, or an Instagram, TikTok, or
          YouTube post.
        </DialogDescription>
      </DialogHeader>

      <form className="flex gap-2" onSubmit={read}>
        <Input
          aria-label="Recipe link"
          className="h-10 min-w-0 flex-1 text-base"
          inputMode="url"
          maxLength={2000}
          onChange={(event) => setUrl(event.target.value)}
          placeholder="https://"
          type="url"
          value={url}
        />
        <Button disabled={pending || !url.trim()} size="lg" type="submit">
          <Link2 aria-hidden="true" />
          {pending && !preview ? "Reading…" : "Read"}
        </Button>
      </form>

      {preview && draft ? (
        <section
          className="grid gap-3"
          data-method={preview.method}
          data-testid="food-import-preview"
        >
          <p
            className={cn(
              "text-sm",
              preview.method === "structured"
                ? "text-success"
                : "text-muted-foreground",
            )}
            role="status"
          >
            {previewSummary(preview)}
          </p>
          {draft.title || draft.imageUrl || draft.description ? (
            <div className="flex gap-3 rounded-lg border p-3">
              {draft.imageUrl ? (
                // Any site's image, shown as it is: next/image would need
                // every host listed.
                // eslint-disable-next-line @next/next/no-img-element
                <img
                  alt=""
                  className="size-20 shrink-0 rounded-md object-cover"
                  loading="lazy"
                  referrerPolicy="no-referrer"
                  src={draft.imageUrl}
                />
              ) : null}
              <div className="min-w-0 space-y-1">
                {draft.title ? (
                  <p className="font-medium wrap-anywhere">{draft.title}</p>
                ) : null}
                {draft.description ? (
                  <p className="text-muted-foreground line-clamp-3 text-sm wrap-anywhere">
                    {draft.description}
                  </p>
                ) : null}
              </div>
            </div>
          ) : null}

          {preview.method !== "structured" ? (
            <Field className="gap-2">
              <FieldLabel htmlFor={`${id}-caption`}>
                Paste the caption
              </FieldLabel>
              <FieldDescription>
                Instagram and TikTok posts can&apos;t be read directly. Copy the
                post&apos;s text in the app and paste it here to fill in the
                ingredients and steps.
              </FieldDescription>
              <Textarea
                className="min-h-28 text-base"
                id={`${id}-caption`}
                maxLength={20000}
                onChange={(event) => setCaption(event.target.value)}
                value={caption}
              />
              <Button
                className="w-fit"
                disabled={pending || !caption.trim()}
                onClick={readCaption}
                size="lg"
                type="button"
                variant="outline"
              >
                Read the caption
              </Button>
              {captionNote ? (
                <p
                  className="text-muted-foreground text-sm"
                  data-testid="food-import-caption-note"
                  role="status"
                >
                  {captionNote}
                </p>
              ) : null}
            </Field>
          ) : null}
        </section>
      ) : null}

      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}

      <DialogFooter>
        <Button
          disabled={pending}
          onClick={onCancel}
          size="lg"
          type="button"
          variant="outline"
        >
          Cancel
        </Button>
        <Button
          disabled={pending || !draft}
          onClick={() =>
            draft &&
            onContinue(draft, {
              url: link,
              imageUrl: draft.imageUrl,
              description: draft.description,
              siteName: draft.siteName,
            })
          }
          size="lg"
          type="button"
        >
          Review and save
        </Button>
      </DialogFooter>
    </div>
  );
}
