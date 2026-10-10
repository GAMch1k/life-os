-- Calendar feeds: the ICS/iCal feeds this integration subscribes to, each one
-- a Calendar source registered through Calendar's integration API under the
-- feed's id as its externalId. A feed holds its configuration (URL, type
-- filter, preset) and its refresh state, so the schedule, the
-- conditional-request validators, and the last outcome survive restarts.
-- The owner's settings of the source (name, enabled, default reminders,
-- default priority) belong to Calendar; name and enabled are mirrored here
-- as Calendar last reported them.
CREATE TABLE feeds (
    -- The source's externalId in Calendar. A feed handed over from Calendar
    -- (calendar-feeds handoff) keeps its Calendar source's id here.
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Calendar's id of the source (its events' source.sourceId); null until
    -- Calendar has registered it.
    source_id UUID UNIQUE,
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    url TEXT NOT NULL CHECK (url <> ''),
    enabled BOOLEAN NOT NULL DEFAULT true,
    -- Type filter: keep only events matching an include term (all when
    -- empty), then drop those matching an exclude term.
    filter_include TEXT[] NOT NULL DEFAULT '{}',
    filter_exclude TEXT[] NOT NULL DEFAULT '{}',
    -- Preset feeds are configured from this service's environment (today
    -- "zaw", from LIFE_OS_ZAW_ICS_URL). preset finds the feed again at every
    -- start; preset_config is the configuration last applied, so a start
    -- changes only what the environment changed since.
    preset TEXT CHECK (preset IS NULL OR btrim(preset) <> ''),
    preset_config JSONB CHECK (preset_config IS NULL OR jsonb_typeof(preset_config) = 'object'),
    -- The source still has to be registered (or registered again) in
    -- Calendar, such as a preset ensured while Calendar was unreachable.
    register_pending BOOLEAN NOT NULL DEFAULT false,
    -- Refresh state.
    next_refresh_at TIMESTAMPTZ,
    refreshing_until TIMESTAMPTZ,
    last_attempt_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_outcome TEXT CHECK (last_outcome IN ('imported', 'not_modified', 'failed')),
    last_error TEXT,
    last_counts JSONB,
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    etag TEXT,
    last_modified TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX feeds_due_idx ON feeds (next_refresh_at) WHERE enabled;
CREATE UNIQUE INDEX feeds_preset_key ON feeds (preset) WHERE preset IS NOT NULL;
