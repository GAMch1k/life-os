import { accountHandler } from "@/lib/auth/account";
import { authRuntime } from "@/lib/auth/runtime";

// Life OS's own account API: who is signed in, sessions, legacy access
// (src/lib/auth/account.ts). It checks the request itself.
export const dynamic = "force-dynamic";

function handle(request: Request) {
  return accountHandler(authRuntime(), request);
}

export const GET = handle;
export const POST = handle;
export const DELETE = handle;
