import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";

/** The host and port of the public address, as an SSH tunnel forwards them. */
function addressOf(publicUrl: string) {
  try {
    const url = new URL(publicUrl);
    return {
      host: url.hostname,
      port: url.port || (url.protocol === "https:" ? "443" : "80"),
    };
  } catch {
    return { host: "127.0.0.1", port: "3000" };
  }
}

/**
 * The login and setup pages on a plain-HTTP page where signing in is off:
 * not this machine's loopback (an SSH tunnel) and not a host the owner
 * allowed (LIFE_OS_INSECURE_LOGIN_HOSTS). With an HTTPS public address it
 * links there. Without one, the public address is this plain-HTTP page
 * itself, so a link would lead back here: it names the setting and the
 * command that allow it, and the SSH tunnel, which always works.
 */
export function PlainHttpNotice({
  path,
  publicUrl,
}: {
  path: "/login" | "/setup";
  publicUrl: string;
}) {
  const allow = (
    <>
      On the server, in the directory with <code>setup.sh</code>, run{" "}
      <code>./setup.sh gateway --allow-http-login</code>, then{" "}
      <code>./setup.sh up</code>: it lists this address in{" "}
      <code>LIFE_OS_INSECURE_LOGIN_HOSTS</code> in <code>.env</code>.
    </>
  );
  if (publicUrl.startsWith("https:")) {
    return (
      <Alert variant="destructive">
        <AlertTitle>Use the HTTPS address</AlertTitle>
        <AlertDescription>
          <p>
            This address is plain HTTP, where signing in is not safe. Open{" "}
            <a href={`${publicUrl}${path}`}>{publicUrl}</a> instead.
          </p>
          <p>
            To sign in at this address anyway, with your password crossing the
            network unencrypted: {allow}
          </p>
        </AlertDescription>
      </Alert>
    );
  }
  const { host, port } = addressOf(publicUrl);
  return (
    <Alert variant="destructive">
      <AlertTitle>Signing in over plain HTTP is off here</AlertTitle>
      <AlertDescription>
        <p>
          Over plain HTTP your password would cross the network unencrypted, so
          this address does not sign in until the server allows it. {allow}
        </p>
        <p>
          Or sign in through an SSH tunnel, which always works:{" "}
          <code>
            ssh -N -L {port}:{host}:{port} you@your-server
          </code>
          , then open{" "}
          <code>
            http://127.0.0.1:{port}
            {path}
          </code>
          . Better still, give Life OS an HTTPS address, with{" "}
          <code>tailscale serve</code> or a reverse proxy.
        </p>
      </AlertDescription>
    </Alert>
  );
}
