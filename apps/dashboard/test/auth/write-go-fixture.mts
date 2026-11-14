// Writes pkg/identity/testdata/dashboard-token.json: an identity token the
// Dashboard's signer minted, with its public key, so that pkg/identity's
// tests prove the Go side reads the Dashboard's tokens. Run it again after
// a change to the token's format:
//
//   node --import tsx test/auth/write-go-fixture.mts
import { generateKeyPairSync } from "node:crypto";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { legacyClaims, spacesOf } from "../../src/lib/auth/check.ts";
import { FIRST_USER_ID } from "../../src/lib/auth/constants.ts";
import { createSigner } from "../../src/lib/auth/identity.ts";

const here = path.dirname(fileURLToPath(import.meta.url));
const kid = "auth-fixture";
const { privateKey } = generateKeyPairSync("ed25519");
const jwk = privateKey.export({ format: "jwk" });
const signer = createSigner(JSON.stringify({ ...jwk, kid }));
const now = 1794657600; // 2026-11-14T12:00:00Z
const token = signer.sign(
  {
    sub: FIRST_USER_ID,
    sid: "6a1f2c1e-2b0e-4f7a-9d55-2f1f1d7a0c11",
    via: "session",
    role: "owner",
    ...spacesOf(FIRST_USER_ID),
  },
  now,
);
const legacy = signer.sign(legacyClaims(), now);
const trustedKeys = JSON.stringify({
  keys: [{ kty: "OKP", crv: "Ed25519", x: jwk.x, kid, use: "sig", alg: "EdDSA" }],
});
const target = path.join(
  here,
  "../../../../pkg/identity/testdata/dashboard-token.json",
);
writeFileSync(
  target,
  `${JSON.stringify({ trustedKeys, token, legacy, now }, null, 2)}\n`,
);
console.log(`wrote ${target}`);
