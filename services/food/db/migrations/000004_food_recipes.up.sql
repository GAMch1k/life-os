-- P15-05 Food: recipes, the weekly meal plan, and shopping items added from
-- recipes (source 'recipe').
--
-- A recipe's title is unique as normalised (the same rules as item names),
-- so a title names one recipe. Tags are kept normalised and without
-- duplicates, in the order given. Steps are plain text, one step per line.
CREATE TABLE recipes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title TEXT NOT NULL CHECK (btrim(title) <> ''),
    normalized_title TEXT NOT NULL CHECK (normalized_title <> ''),
    servings INTEGER CHECK (servings IS NULL OR servings BETWEEN 1 AND 100),
    steps TEXT,
    tags TEXT[] NOT NULL DEFAULT '{}',
    source_url TEXT,
    note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
);
CREATE UNIQUE INDEX recipes_normalized_title_key ON recipes (normalized_title);
CREATE INDEX recipes_tags_idx ON recipes USING gin (tags);

-- A recipe's ingredients, in order. An update replaces them all.
CREATE TABLE recipe_ingredients (
    recipe_id UUID NOT NULL REFERENCES recipes (id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    normalized_name TEXT NOT NULL CHECK (normalized_name <> ''),
    amount TEXT,
    note TEXT,
    PRIMARY KEY (recipe_id, position)
);
CREATE INDEX recipe_ingredients_name_idx ON recipe_ingredients (normalized_name);

-- The meal plan: at most one entry per day and slot. An entry names a
-- recipe or is free text; title is the recipe's title (kept in step when the
-- recipe is renamed) or the text, so an entry whose recipe is deleted stays
-- as plain text (recipe_id becomes NULL).
CREATE TABLE meal_plan_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    day DATE NOT NULL,
    slot TEXT NOT NULL CHECK (slot IN ('breakfast', 'lunch', 'dinner')),
    slot_rank SMALLINT NOT NULL GENERATED ALWAYS AS (CASE slot
        WHEN 'breakfast' THEN 1 WHEN 'lunch' THEN 2 ELSE 3 END) STORED,
    recipe_id UUID REFERENCES recipes (id) ON DELETE SET NULL,
    title TEXT NOT NULL CHECK (btrim(title) <> ''),
    servings INTEGER CHECK (servings IS NULL OR servings BETWEEN 1 AND 100),
    note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
);
CREATE UNIQUE INDEX meal_plan_entries_slot_key ON meal_plan_entries (day, slot);
CREATE INDEX meal_plan_entries_recipe_idx ON meal_plan_entries (recipe_id) WHERE recipe_id IS NOT NULL;

-- Items added from recipes have source 'recipe' (sourceRef: the recipe's
-- id), also once put away in the pantry.
ALTER TABLE shopping_items DROP CONSTRAINT shopping_items_source_check;
ALTER TABLE shopping_items ADD CONSTRAINT shopping_items_source_check CHECK (source IN ('manual', 'capture', 'recipe'));
ALTER TABLE pantry_items DROP CONSTRAINT pantry_items_source_check;
ALTER TABLE pantry_items ADD CONSTRAINT pantry_items_source_check CHECK (source IN ('manual', 'capture', 'recipe'));
