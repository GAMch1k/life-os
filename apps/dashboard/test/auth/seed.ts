import { randomBytes } from "node:crypto";

import { Client } from "pg";

import { hashPassword } from "../../src/lib/auth/password";

/**
 * A second account, straight into auth_db: P21-03 opens no way to create one
 * (invites come with P21-07), but the suite needs one to ban, promote, and
 * delete.
 */
export async function seedUser(
  url: string,
  {
    username,
    password,
    role = "member",
  }: { username: string; password: string; role?: "admin" | "member" },
): Promise<string> {
  const client = new Client({ connectionString: url });
  await client.connect();
  try {
    const { rows } = await client.query<{ id: string }>(
      `INSERT INTO "user" (name, email, "emailVerified", username, "displayUsername", role)
       VALUES ($1, $2, false, lower($1), $1, $3) RETURNING id`,
      [username, `u-${randomBytes(9).toString("hex")}@users.invalid`, role],
    );
    const id = rows[0].id;
    await client.query(
      `INSERT INTO account ("accountId", "providerId", "userId", password, "updatedAt")
       VALUES ($1, 'credential', $2, $3, now())`,
      [id, id, await hashPassword(password)],
    );
    return id;
  } finally {
    await client.end();
  }
}
