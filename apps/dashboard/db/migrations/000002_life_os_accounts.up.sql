-- Life OS's part of auth_db (docs/architecture/multi-user.md): the rules
-- Better Auth's schema leaves out, the setup codes, and the server settings.

-- Every account has a server role; exactly one is the owner.
UPDATE "user" SET "role" = 'member' WHERE "role" IS NULL;
ALTER TABLE "user" ALTER COLUMN "role" SET DEFAULT 'member';
ALTER TABLE "user" ALTER COLUMN "role" SET NOT NULL;
ALTER TABLE "user" ADD CONSTRAINT user_role_check CHECK ("role" IN ('owner', 'admin', 'member'));
CREATE UNIQUE INDEX user_one_owner_idx ON "user" ("role") WHERE "role" = 'owner';

-- authz_version: bumped by every change an identity token reflects, so the
-- auth check re-mints a token at once instead of after five minutes. A
-- trigger, so that every path counts: Better Auth's admin endpoints, the
-- host's ./setup.sh, and later tickets' own changes.
UPDATE "user" SET "authzVersion" = 1 WHERE "authzVersion" IS NULL;
ALTER TABLE "user" ALTER COLUMN "authzVersion" SET DEFAULT 1;
ALTER TABLE "user" ALTER COLUMN "authzVersion" SET NOT NULL;

CREATE FUNCTION lifeos_bump_authz_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW."role" IS DISTINCT FROM OLD."role"
     OR NEW."banned" IS DISTINCT FROM OLD."banned"
     OR NEW."banExpires" IS DISTINCT FROM OLD."banExpires" THEN
    NEW."authzVersion" := OLD."authzVersion" + 1;
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER user_authz_version
  BEFORE UPDATE ON "user"
  FOR EACH ROW EXECUTE FUNCTION lifeos_bump_authz_version();

-- One-time setup codes for the first account (./setup.sh setup-code): only
-- the SHA-256 of a code is kept, valid 24 hours, once.
CREATE TABLE setup_codes (
  code_hash TEXT PRIMARY KEY CHECK (code_hash ~ '^[0-9a-f]{64}$'),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  used_at TIMESTAMPTZ,
  used_by UUID REFERENCES "user" ("id") ON DELETE SET NULL
);

-- Server-wide settings, such as legacy_access. One row per key.
CREATE TABLE server_settings (
  key TEXT PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{0,63}$'),
  value JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_by UUID REFERENCES "user" ("id") ON DELETE SET NULL
);
