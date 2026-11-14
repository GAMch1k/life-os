import crypto from "node:crypto";

/**
 * Stand-ins for a person's devices in the auth flow suite: an authenticator
 * app (RFC 6238 TOTP) and a software WebAuthn authenticator (ES256, "none"
 * attestation). Test tools only, never a real security device.
 */

function base32decode(value: string): Buffer {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  const out: number[] = [];
  for (const c of value.replace(/=+$/, "").toUpperCase()) {
    const v = alphabet.indexOf(c);
    if (v < 0) continue;
    bits += v.toString(2).padStart(5, "0");
  }
  for (let i = 0; i + 8 <= bits.length; i += 8) {
    out.push(parseInt(bits.slice(i, i + 8), 2));
  }
  return Buffer.from(out);
}

/** The current TOTP code (SHA-1, 6 digits, 30 s) of a secret or otpauth URI. */
export function totp(secretOrUri: string, at = Date.now()): string {
  const secret = secretOrUri.startsWith("otpauth://")
    ? (new URL(secretOrUri).searchParams.get("secret") ?? "")
    : secretOrUri;
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 1000 / 30)));
  const h = crypto
    .createHmac("sha1", base32decode(secret))
    .update(counter)
    .digest();
  const o = h[h.length - 1] & 0xf;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1e6).padStart(6, "0");
}

const b64u = (b: Buffer | Uint8Array) => Buffer.from(b).toString("base64url");
const fromB64u = (s: string) => Buffer.from(s, "base64url");

function head(major: number, n: number) {
  if (n < 24) return Buffer.from([(major << 5) | n]);
  if (n < 256) return Buffer.from([(major << 5) | 24, n]);
  const b = Buffer.alloc(3);
  b[0] = (major << 5) | 25;
  b.writeUInt16BE(n, 1);
  return b;
}

type Cbor = number | string | Buffer | Map<Cbor, Cbor>;

function cbor(v: Cbor): Buffer {
  if (typeof v === "number") return v >= 0 ? head(0, v) : head(1, -1 - v);
  if (Buffer.isBuffer(v)) return Buffer.concat([head(2, v.length), v]);
  if (typeof v === "string") {
    const b = Buffer.from(v, "utf8");
    return Buffer.concat([head(3, b.length), b]);
  }
  const parts: Buffer[] = [head(5, v.size)];
  for (const [k, val] of v) parts.push(cbor(k), cbor(val));
  return Buffer.concat(parts);
}

type CreationOptions = {
  challenge: string;
  rp?: { id?: string };
  user?: { id?: string };
};
type RequestOptions = { challenge: string; rpId?: string };

export class SoftAuthenticator {
  private readonly priv: crypto.KeyObject;
  private readonly jwk: crypto.JsonWebKey;
  private readonly credId = crypto.randomBytes(32);
  private count = 0;
  private rpId = "";
  private userHandle: string | undefined;

  constructor() {
    const { privateKey, publicKey } = crypto.generateKeyPairSync("ec", {
      namedCurve: "P-256",
    });
    this.priv = privateKey;
    this.jwk = publicKey.export({ format: "jwk" });
  }

  create(options: CreationOptions, origin: string) {
    const rpId = options.rp?.id || new URL(origin).hostname;
    this.rpId = rpId;
    this.userHandle = options.user?.id;
    const clientDataJSON = Buffer.from(
      JSON.stringify({
        type: "webauthn.create",
        challenge: options.challenge,
        origin,
        crossOrigin: false,
      }),
    );
    const cose = new Map<Cbor, Cbor>([
      [1, 2],
      [3, -7],
      [-1, 1],
      [-2, fromB64u(this.jwk.x as string)],
      [-3, fromB64u(this.jwk.y as string)],
    ]);
    const credIdLen = Buffer.alloc(2);
    credIdLen.writeUInt16BE(this.credId.length);
    const authData = Buffer.concat([
      crypto.createHash("sha256").update(rpId).digest(),
      Buffer.from([0x45]),
      Buffer.alloc(4),
      Buffer.alloc(16),
      credIdLen,
      this.credId,
      cbor(cose),
    ]);
    const attestationObject = cbor(
      new Map<Cbor, Cbor>([
        ["fmt", "none"],
        ["attStmt", new Map()],
        ["authData", authData],
      ]),
    );
    return {
      id: b64u(this.credId),
      rawId: b64u(this.credId),
      type: "public-key",
      response: {
        clientDataJSON: b64u(clientDataJSON),
        attestationObject: b64u(attestationObject),
        transports: ["internal"],
        publicKeyAlgorithm: -7,
        authenticatorData: b64u(authData),
        publicKey: b64u(
          crypto
            .createPublicKey({ key: this.jwk, format: "jwk" })
            .export({ type: "spki", format: "der" }),
        ),
      },
      clientExtensionResults: {},
      authenticatorAttachment: "platform",
    };
  }

  get(options: RequestOptions, origin: string) {
    const rpId = options.rpId || this.rpId;
    this.count += 1;
    const cnt = Buffer.alloc(4);
    cnt.writeUInt32BE(this.count);
    const authData = Buffer.concat([
      crypto.createHash("sha256").update(rpId).digest(),
      Buffer.from([0x05]),
      cnt,
    ]);
    const clientDataJSON = Buffer.from(
      JSON.stringify({
        type: "webauthn.get",
        challenge: options.challenge,
        origin,
        crossOrigin: false,
      }),
    );
    const signature = crypto.sign(
      "sha256",
      Buffer.concat([
        authData,
        crypto.createHash("sha256").update(clientDataJSON).digest(),
      ]),
      this.priv,
    );
    return {
      id: b64u(this.credId),
      rawId: b64u(this.credId),
      type: "public-key",
      response: {
        clientDataJSON: b64u(clientDataJSON),
        authenticatorData: b64u(authData),
        signature: b64u(signature),
        userHandle: this.userHandle,
      },
      clientExtensionResults: {},
      authenticatorAttachment: "platform",
    };
  }
}
