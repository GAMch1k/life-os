import { checkHandler } from "@/lib/auth/handlers";
import { authRuntime } from "@/lib/auth/runtime";

// The gateway's auth_request (docs/architecture/multi-user.md#the-gateway):
// 204 with X-Life-OS-Identity, 401, or 403. The gateway answers /internal/
// with 404 from outside, so only its own subrequest reaches this.
export const dynamic = "force-dynamic";

function handle(request: Request) {
  return checkHandler(authRuntime(), request);
}

export const GET = handle;
export const HEAD = handle;
export const POST = handle;
export const PUT = handle;
export const PATCH = handle;
export const DELETE = handle;
export const OPTIONS = handle;
