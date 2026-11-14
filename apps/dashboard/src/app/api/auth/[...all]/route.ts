import { authHandler } from "@/lib/auth/handlers";
import { authRuntime } from "@/lib/auth/runtime";

// Better Auth: sign-in, two-factor, passkeys, the first account's sign-up,
// sessions (src/lib/auth/server.ts). Public, behind the gateway's strict
// rate limit for /api/auth/ (infra/nginx/life-os.conf.template).
export const dynamic = "force-dynamic";

function handle(request: Request) {
  return authHandler(authRuntime(), request);
}

export const GET = handle;
export const POST = handle;
