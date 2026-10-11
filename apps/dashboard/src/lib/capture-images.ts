import { inboxClient } from "@/lib/api/inbox-client";
import type { InboxItem } from "@/lib/inbox-summary";

// Photos with a capture: attached in the composer, or shared to Life OS.
// Inbox stores them (POST /v1/media) and the capture names them in
// mediaReferences as "inbox-media:<sha256>"; the AI reads them with the
// capture's text.

/** How many photos one capture can have (Inbox's limit). */
export const MAX_CAPTURE_IMAGES = 5;

/** The largest photo Inbox stores (INBOX_MEDIA_MAX_BYTES, 10 MiB). */
export const MAX_IMAGE_BYTES = 10 * 1024 * 1024;

/** What the photo pickers offer; Inbox keeps JPEG, PNG, and WebP. */
export const IMAGE_ACCEPT = "image/*";

const REFERENCE = /^inbox-media:([0-9a-f]{64})$/;

/**
 * Where the browser loads a capture's photo: Inbox's media endpoint through
 * the gateway (or the Dashboard's /api/inbox proxy), so never outside
 * Cloudflare Access. Undefined for a reference that is not an Inbox photo.
 */
export function mediaUrl(reference: string, base = "/api/inbox") {
  const hash = REFERENCE.exec(reference)?.[1];
  return hash ? `${base}/v1/media/${hash}` : undefined;
}

/** The capture's photos, by their references. */
export function captureImages(item: Pick<InboxItem, "mediaReferences">) {
  return (item.mediaReferences ?? []).filter((reference) =>
    REFERENCE.test(reference),
  );
}

/** The text of a capture that is only photos. */
export function photosContent(count: number) {
  return count === 1 ? "Photo" : `${count} photos`;
}

/** Why photos cannot be captured: Inbox's error codes for them. */
export type ImageProblem =
  | "too_many_images"
  | "heic_not_supported"
  | "unsupported_image"
  | "image_too_large";

/** Why Inbox refused a photo, by its error code, in the owner's words. */
export function imageErrorMessage(code: string | undefined) {
  switch (code) {
    case "image_too_large":
      return `A photo is too large: each can be at most ${MAX_IMAGE_BYTES / 1024 / 1024} MB.`;
    case "heic_not_supported":
      return "HEIC photos are not supported yet. Share the photo as a JPEG (on the phone's camera, choose Most compatible).";
    case "unsupported_image":
      return "Photos must be JPEG, PNG, or WebP.";
    case "too_many_images":
      return `A capture can have at most ${MAX_CAPTURE_IMAGES} photos.`;
    default:
      return "Could not save the photos. Please try again.";
  }
}

type PickedFile = Pick<File, "name" | "size" | "type">;

/** A HEIC/HEIF photo, by its type or, when the browser gives none, its name. */
function isHeic(file: PickedFile) {
  return (
    /^image\/hei[cf]/i.test(file.type) || /\.(heic|heif)$/i.test(file.name)
  );
}

/**
 * Checks photos before uploading them, as Inbox will: at most five with the
 * ones already attached, each at most 10 MB, no HEIC. Returns why not, or
 * undefined when they can go.
 */
export function photosProblem(
  files: PickedFile[],
  alreadyAttached = 0,
): ImageProblem | undefined {
  if (files.length + alreadyAttached > MAX_CAPTURE_IMAGES) {
    return "too_many_images";
  }
  if (files.some(isHeic)) return "heic_not_supported";
  if (files.some((file) => file.type && !file.type.startsWith("image/"))) {
    return "unsupported_image";
  }
  if (files.some((file) => file.size === 0 || file.size > MAX_IMAGE_BYTES)) {
    return "image_too_large";
  }
  return undefined;
}

/** The references of the uploaded photos, or Inbox's error code, if any. */
export type UploadResult =
  { references: string[] } | { references?: undefined; code?: string };

/**
 * Uploads photos to Inbox and returns their references, in order, or why
 * Inbox refused them. `perRequest` limits the photos of one request: the
 * browser sends one at a time, within the gateway's request size limit; the
 * server (Share) sends them together on the private network.
 */
export async function uploadImages(
  files: Blob[],
  { client = inboxClient, perRequest = 1 } = {},
): Promise<UploadResult> {
  const references: string[] = [];
  for (let start = 0; start < files.length; start += perRequest) {
    const batch = files.slice(start, start + perRequest);
    try {
      const { data, error } = await client.POST("/v1/media", {
        // The generated type names binary fields as strings; the body is
        // multipart, built by the serializer.
        body: { images: [] },
        bodySerializer: () => {
          const form = new FormData();
          for (const file of batch) form.append("images", file);
          return form;
        },
      });
      if (!data) {
        const code = (error as { error?: { code?: string } } | undefined)?.error
          ?.code;
        return { code };
      }
      references.push(...data.items.map((item) => item.reference));
    } catch {
      return {};
    }
  }
  return { references };
}
