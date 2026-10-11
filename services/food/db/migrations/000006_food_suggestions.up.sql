-- P15-09 Food: cooking preferences, recipes' cooking methods, and recipe
-- ideas (unsaved drafts a generator such as the AI suggests).
--
-- The preferences are one row: the preferred cooking methods (the air fryer
-- first), the disliked ingredients (names as written; Food compares them
-- normalised), and free dietary notes.
CREATE TABLE food_preferences (
    id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    methods TEXT[] NOT NULL DEFAULT '{}'
        CHECK (methods <@ ARRAY['air_fryer', 'oven', 'stovetop', 'slow_cooker', 'grill', 'microwave', 'no_cook']::text[]),
    dislikes TEXT[] NOT NULL DEFAULT '{}',
    dietary_notes TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT NOT NULL
);
INSERT INTO food_preferences (methods, updated_by) VALUES ('{air_fryer}', 'system:food');

-- A recipe's cooking methods as the owner set them; NULL means Food infers
-- them from the title, tags, and steps whenever it reads the recipe, so
-- existing and imported recipes need no backfill.
ALTER TABLE recipes ADD COLUMN methods TEXT[]
    CHECK (methods IS NULL OR methods <@ ARRAY['air_fryer', 'oven', 'stovetop', 'slow_cooker', 'grill', 'microwave', 'no_cook']::text[]);

-- Recipe ideas: drafts that are not recipes yet, each with why it was
-- suggested. `source` says where it came from ('ai': written by the AI from
-- the pantry and the preferences; 'web': found on a site, with its link).
-- An idea is new until the owner saves it (it becomes a recipe) or
-- dismisses it. One new idea per normalised title.
CREATE TABLE food_ideas (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title TEXT NOT NULL CHECK (btrim(title) <> ''),
    normalized_title TEXT NOT NULL CHECK (normalized_title <> ''),
    servings INTEGER CHECK (servings IS NULL OR servings BETWEEN 1 AND 100),
    ingredients JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(ingredients) = 'array'),
    steps TEXT,
    tags TEXT[] NOT NULL DEFAULT '{}',
    methods TEXT[] NOT NULL DEFAULT '{}'
        CHECK (methods <@ ARRAY['air_fryer', 'oven', 'stovetop', 'slow_cooker', 'grill', 'microwave', 'no_cook']::text[]),
    source TEXT NOT NULL CHECK (source IN ('ai', 'web')),
    source_url TEXT,
    reason TEXT NOT NULL CHECK (btrim(reason) <> ''),
    state TEXT NOT NULL DEFAULT 'new' CHECK (state IN ('new', 'saved', 'dismissed')),
    recipe_id UUID REFERENCES recipes (id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
);
CREATE UNIQUE INDEX food_ideas_new_title_key ON food_ideas (normalized_title) WHERE state = 'new';
CREATE INDEX food_ideas_state_idx ON food_ideas (state, created_at DESC);
