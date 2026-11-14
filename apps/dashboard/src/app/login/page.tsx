import type { Metadata } from "next";
import { redirect } from "next/navigation";

import { LoginForm } from "@/components/login-form";
import { PairDevice } from "@/components/pair-device";
import { oauthLoginContinuation } from "@/lib/auth/oauth-pages";
import { loginContext, safeNext } from "@/lib/auth/page-context";
import { isChallenge, isPairingState, pairingLink } from "@/lib/auth/pairing";
import { authRuntime } from "@/lib/auth/runtime";

export const metadata: Metadata = { title: "Sign in · Life OS" };
export const dynamic = "force-dynamic";

/**
 * Sign in: password, then TOTP or a recovery code; or a passkey.
 *
 * Two flows end here too (P21-06):
 *  - /login?pair=1&challenge=…&state=… is the phone app's Custom Tab
 *    (docs/api/devices.md#flow-2-custom-tab-a-phone-alone): signed in, it
 *    asks to pair the phone and sends the browser to lifeos://pair, and
 *    nowhere else.
 *  - An MCP client's OAuth request (signed by the OAuth provider): signed
 *    in, the browser goes on to the provider's authorize endpoint.
 */
export default async function LoginPage({ searchParams }: PageProps<"/login">) {
  const params = await searchParams;
  const context = await loginContext();

  if (params.pair === "1") {
    const challenge = params.challenge;
    const state = params.state;
    const valid =
      isChallenge(challenge) && (state === undefined || isPairingState(state));
    const query = new URLSearchParams({ pair: "1" });
    if (isChallenge(challenge)) query.set("challenge", challenge);
    if (isPairingState(state)) query.set("state", state);
    if (context.signedIn || !valid) {
      return (
        <PairDevice
          cancelLink={pairingLink(
            context.publicUrl,
            { error: "access_denied" },
            isPairingState(state) ? state : undefined,
          )}
          challenge={valid ? (challenge as string) : null}
          publicUrl={context.publicUrl}
          state={isPairingState(state) ? state : undefined}
        />
      );
    }
    return <LoginForm context={context} next={`/login?${query.toString()}`} />;
  }

  const authorize = await oauthLoginContinuation(authRuntime(), params);
  if (authorize) {
    if (context.signedIn) redirect(authorize);
    return <LoginForm context={context} navigate="document" next={authorize} />;
  }

  const next = safeNext(params.next);
  if (context.signedIn) redirect(next);
  return <LoginForm context={context} next={next} />;
}
