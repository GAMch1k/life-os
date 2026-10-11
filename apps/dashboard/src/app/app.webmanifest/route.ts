import { webAppManifest } from "@/lib/web-app";

// The web app manifest (see src/lib/web-app.ts). A route handler rather than
// app/manifest.ts: the link Next renders for that convention cannot carry
// crossorigin="use-credentials", which Cloudflare Access needs, so the root
// layout links this one itself.
export const dynamic = "force-static";

export function GET() {
  return new Response(JSON.stringify(webAppManifest()), {
    headers: { "content-type": "application/manifest+json; charset=utf-8" },
  });
}
