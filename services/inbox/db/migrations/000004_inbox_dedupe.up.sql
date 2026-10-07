ALTER TABLE inbox_items ADD COLUMN source_dedupe_key TEXT;
CREATE UNIQUE INDEX inbox_items_source_dedupe_key_idx ON inbox_items (source_dedupe_key) WHERE source_dedupe_key IS NOT NULL;
