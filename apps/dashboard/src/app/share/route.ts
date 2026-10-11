import { type NextRequest, NextResponse } from "next/server";

import { createInboxClient } from "@/lib/api/inbox-client";
import {
  MAX_CAPTURE_IMAGES,
  photosContent,
  photosProblem,
  uploadImages,
} from "@/lib/capture-images";
import {
  isShareFromApp,
  photoShareError,
  SHARE_FIELDS,
  type ShareError,
  shareContent,
  shareMetadata,
} from "@/lib/share";
import { isValidTimeZone, TIME_ZONE_COOKIE } from "@/lib/time-zone";

// Share → Life OS. The installed Dashboard's share target (see
// src/lib/web-app.ts): the phone's share sheet posts the title, text, link,
// and photos of what was shared here, as multipart form data. The photos go
// to Inbox's media store first; then it all becomes one ordinary Inbox
// capture (source "web", sourceMetadata.shared) naming them, which the AI
// processes like any other, and the app shows /share/saved.

export const dynamic = "force-dynamic";

/** A 303 to a page of the app: the share sheet's POST becomes a GET there. */
function seeOther(location: string) {
  // Relative, so it stays on the host the owner used (behind the gateway the
  // request URL names the Dashboard container).
  return new NextResponse(null, { status: 303, headers: { location } });
}

function failed(error: ShareError) {
  return seeOther(`/share/saved?error=${error}`);
}

function field(form: FormData, name: string) {
  const value = form.get(name);
  return typeof value === "string" ? value : undefined;
}

export async function POST(request: NextRequest) {
  if (!isShareFromApp(request.headers)) {
    return NextResponse.json(
      {
        error: {
          code: "forbidden",
          message: "Shares are accepted only from the Life OS app",
        },
      },
      { status: 403 },
    );
  }
  let form: FormData;
  try {
    form = await request.formData();
  } catch {
    return failed("unreadable");
  }
  // A share without photos may still send an empty file part.
  const images = form
    .getAll(SHARE_FIELDS.images)
    .filter(
      (value): value is File => typeof value !== "string" && value.size > 0,
    );
  const text = shareContent({
    title: field(form, SHARE_FIELDS.title),
    text: field(form, SHARE_FIELDS.text),
    url: field(form, SHARE_FIELDS.url),
  });
  if (!text && !images.length) return failed("empty");
  const problem = photosProblem(images);
  if (problem) return failed(problem);

  const inbox = createInboxClient();
  let mediaReferences: string[] = [];
  if (images.length) {
    // Together, on the private network: Inbox takes five at once.
    const uploaded = await uploadImages(images, {
      client: inbox,
      perRequest: MAX_CAPTURE_IMAGES,
    });
    if (!uploaded.references) return failed(photoShareError(uploaded.code));
    mediaReferences = uploaded.references;
  }
  const content = text || photosContent(mediaReferences.length);
  const cookie = request.cookies.get(TIME_ZONE_COOKIE)?.value;
  const timeZone = cookie && isValidTimeZone(cookie) ? cookie : undefined;
  try {
    // Written as the owner (the client sends X-Life-OS-Actor: user).
    const { data } = await inbox.POST("/v1/inbox-items", {
      body: {
        content,
        source: "web",
        sourceMetadata: shareMetadata(timeZone),
        ...(mediaReferences.length ? { mediaReferences } : {}),
      },
    });
    if (!data) return failed("inbox");
    return seeOther(`/share/saved?item=${encodeURIComponent(data.id)}`);
  } catch {
    return failed("inbox");
  }
}

/** Opening /share itself explains how sharing works. */
export function GET() {
  return seeOther("/share/about");
}
