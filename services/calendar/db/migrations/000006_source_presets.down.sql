DROP INDEX IF EXISTS calendar_sources_preset_key;
ALTER TABLE calendar_sources DROP COLUMN IF EXISTS preset_config, DROP COLUMN IF EXISTS preset;
