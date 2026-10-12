-- The version of Banking's transaction mapping (BUG-38: card settlements
-- and PayPal purchases name the merchant) that the connection's last full
-- read used. While it is older than the code's (banking.MappingVersion),
-- the next sync reads the whole BANKING_INITIAL_DAYS window instead of the
-- days since the cursor, so Finance gets every transaction it holds again
-- with the new mapping; a successful push of such a read records the
-- version. Existing connections start at 1, the first mapping.
ALTER TABLE bank_connections
    ADD COLUMN mapping_version INTEGER NOT NULL DEFAULT 1 CHECK (mapping_version >= 1);
