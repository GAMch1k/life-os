import { type NextRequest, NextResponse } from "next/server";

import { createInboxClient } from "@/lib/api/inbox-client";
import {
  isShareFromApp,
  SHARE_FIELDS,
  type ShareError,
  shareContent,
  shareMetadata,
} from "@/lib/share";
import { isValidTimeZone, TIME_ZONE_COOKIE } from "@/lib/time-zone";

// Share → Life OS. The installed Dashboard's share target (see
// src/lib/web-app.ts): the phone's share sheet posts the title, text, and
// link of what was shared here, as multipart form data. It becomes an
// ordinary Inbox capture (source "web", sourceMetadata.shared), which the AI
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
  const content = shareContent({
    title: field(form, SHARE_FIELDS.title),
    text: field(form, SHARE_FIELDS.text),
    url: field(form, SHARE_FIELDS.url),
  });
  if (!content) return failed("empty");

  const cookie = request.cookies.get(TIME_ZONE_COOKIE)?.value;
  const timeZone = cookie && isValidTimeZone(cookie) ? cookie : undefined;
  try {
    // Written as the owner (the client sends X-Life-OS-Actor: user).
    const { data } = await createInboxClient().POST("/v1/inbox-items", {
      body: { content, source: "web", sourceMetadata: shareMetadata(timeZone) },
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
