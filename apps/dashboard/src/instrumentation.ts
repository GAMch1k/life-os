/**
 * Runs once when the Dashboard's server starts (never during `next build`).
 * The auth runtime is made here rather than on the first request, so that a
 * wrong sign-in setting, such as a signing key LIFE_OS_TRUSTED_KEYS does not
 * match, stops the server at once with its reason in the log instead of
 * failing every request (docs/install/accounts.md#keys).
 */
export async function register() {
  if (process.env.NEXT_RUNTIME !== "nodejs") return;
  const { authRuntime } = await import("./lib/auth/runtime");
  try {
    authRuntime();
  } catch (error) {
    console.error(
      `Life OS's sign-in settings are wrong: ${(error as Error).message}`,
    );
    throw error;
  }
}
