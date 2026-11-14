// Prints the SQL Better Auth's migration generator would run on auth_db
// after the committed migrations: what a new migration must add, statement
// for statement (a development aid, as the auth flow suite's schema test
// fails until the migrations match).
//
//   AUTH_TEST_DATABASE_URL=postgres://… node --import tsx test/auth/print-schema.mts
import { getMigrations } from "better-auth/db/migration";

import { adminUrl, freshDatabase, runtime } from "./harness";

const db = await freshDatabase(adminUrl()!);
const rt = runtime(db.url);
try {
  const { compileMigrations, toBeCreated, toBeAdded } = await getMigrations(
    rt.auth.options,
  );
  console.log("-- create:", toBeCreated.map((t) => t.table).join(", "));
  console.log(
    "-- add:",
    toBeAdded
      .map((t) => `${t.table}(${Object.keys(t.fields).join(",")})`)
      .join("; "),
  );
  console.log(await compileMigrations());
} finally {
  await rt.pool.end();
  await db.drop();
}
