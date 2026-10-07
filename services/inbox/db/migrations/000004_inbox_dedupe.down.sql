DROP INDEX IF EXISTS inbox_items_source_dedupe_key_idx;
ALTER TABLE inbox_items DROP COLUMN IF EXISTS source_dedupe_key;
