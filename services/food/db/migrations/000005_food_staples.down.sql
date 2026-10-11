-- Reverses 000005_food_staples: drops the staples and the running-low flag;
-- items a staple put on the list stay, as manual ones.
UPDATE pantry_items SET source = 'manual' WHERE source = 'staple';
ALTER TABLE pantry_items DROP CONSTRAINT pantry_items_source_check;
ALTER TABLE pantry_items ADD CONSTRAINT pantry_items_source_check CHECK (source IN ('manual', 'capture', 'recipe'));
UPDATE shopping_items SET source = 'manual' WHERE source = 'staple';
ALTER TABLE shopping_items DROP CONSTRAINT shopping_items_source_check;
ALTER TABLE shopping_items ADD CONSTRAINT shopping_items_source_check CHECK (source IN ('manual', 'capture', 'recipe'));
DROP INDEX IF EXISTS pantry_items_running_low_idx;
ALTER TABLE pantry_items DROP CONSTRAINT IF EXISTS pantry_items_running_low_check;
ALTER TABLE pantry_items DROP COLUMN IF EXISTS running_low_at;
DROP TABLE IF EXISTS staples;
