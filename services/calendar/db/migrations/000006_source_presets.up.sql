-- Preset sources: sources Calendar keeps configured from its own
-- environment (today "zaw", the ZAW waste-collection feed named by
-- LIFE_OS_ZAW_ICS_URL), instead of being added through the API.
-- preset finds the source again at every start, whatever its name or URL,
-- and holds at most one source per preset. preset_config is the
-- configuration last applied from the environment: a start changes only what
-- the environment changed since, so edits made through the API stay.
ALTER TABLE calendar_sources
    ADD COLUMN preset TEXT CHECK (preset IS NULL OR btrim(preset) <> ''),
    ADD COLUMN preset_config JSONB CHECK (preset_config IS NULL OR jsonb_typeof(preset_config) = 'object');

CREATE UNIQUE INDEX calendar_sources_preset_key ON calendar_sources (preset) WHERE preset IS NOT NULL;
