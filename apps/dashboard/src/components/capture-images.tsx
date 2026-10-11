"use client";

import { useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { mediaUrl } from "@/lib/capture-images";

/**
 * A capture's photos as small thumbnails; tapping one shows it whole in a
 * dialog. The images come from Inbox through the gateway, under the same
 * Cloudflare Access login as the page.
 */
export function CaptureImageStrip({
  label,
  references,
}: {
  /** The capture's text, naming whose photos these are. */
  label: string;
  references: string[];
}) {
  const [shown, setShown] = useState<number | null>(null);
  const images = references.flatMap((reference) => {
    const src = mediaUrl(reference);
    return src ? [src] : [];
  });
  if (!images.length) return null;
  const count = images.length;
  return (
    <>
      <ul aria-label="Photos" className="mt-1.5 flex flex-wrap gap-1.5">
        {images.map((src, index) => (
          <li key={src}>
            <button
              aria-label={`View photo ${index + 1} of ${count}`}
              className="focus-visible:ring-ring/50 block overflow-hidden rounded-md border outline-none focus-visible:ring-3"
              onClick={() => setShown(index)}
              type="button"
            >
              {/* A plain img: the image optimizer would fetch it without the
                  owner's Access cookie. */}
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img
                alt=""
                className="bg-muted size-16 object-cover"
                decoding="async"
                loading="lazy"
                src={src}
              />
            </button>
          </li>
        ))}
      </ul>
      <Dialog
        onOpenChange={(open) => {
          if (!open) setShown(null);
        }}
        open={shown !== null}
      >
        <DialogContent className="sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>
              {count > 1 ? `Photo ${(shown ?? 0) + 1} of ${count}` : "Photo"}
            </DialogTitle>
          </DialogHeader>
          {shown !== null ? (
            // eslint-disable-next-line @next/next/no-img-element
            <img
              alt={`Photo with the capture “${label}”`}
              className="max-h-[75dvh] w-full rounded-md object-contain"
              src={images[shown]}
            />
          ) : null}
        </DialogContent>
      </Dialog>
    </>
  );
}
