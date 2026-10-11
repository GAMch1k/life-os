-- P15-08 Food: staples, the things the owner always wants at home, and
-- pantry items running low. When an item whose name is a staple runs low,
-- is used up, or is thrown away, Food puts the staple on the shopping list
-- (source 'staple', sourceRef the staple's id).
--
-- A staple is a name, unique as normalised like item names, independent of
-- pantry rows: "Milch" stays a staple when none is at home. Its amount,
-- store, and aisle are what the list gets.
CREATE TABLE staples (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    normalized_name TEXT NOT NULL CHECK (normalized_name <> ''),
    amount TEXT,
    store_id UUID REFERENCES stores (id),
    aisle TEXT CHECK (aisle IS NULL OR aisle IN ('produce', 'bakery', 'dairy', 'meat_fish', 'frozen', 'pantry', 'drinks', 'snacks', 'household', 'drugstore', 'other')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
);
CREATE UNIQUE INDEX staples_normalized_name_key ON staples (normalized_name);

-- An in-stock pantry item may be flagged as running low; ending it (used
-- up, thrown away) or restocking it clears the flag.
ALTER TABLE pantry_items ADD COLUMN running_low_at TIMESTAMPTZ;
ALTER TABLE pantry_items ADD CONSTRAINT pantry_items_running_low_check CHECK (running_low_at IS NULL OR state = 'in_stock');
CREATE INDEX pantry_items_running_low_idx ON pantry_items (running_low_at) WHERE running_low_at IS NOT NULL;

-- Items a staple puts on the list have source 'staple', also once put away.
ALTER TABLE shopping_items DROP CONSTRAINT shopping_items_source_check;
ALTER TABLE shopping_items ADD CONSTRAINT shopping_items_source_check CHECK (source IN ('manual', 'capture', 'recipe', 'staple'));
ALTER TABLE pantry_items DROP CONSTRAINT pantry_items_source_check;
ALTER TABLE pantry_items ADD CONSTRAINT pantry_items_source_check CHECK (source IN ('manual', 'capture', 'recipe', 'staple'));
