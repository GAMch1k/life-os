-- Reverses 000004_food_recipes: drops the recipes and the meal plan; items
-- added from recipes stay, as manual ones.
UPDATE pantry_items SET source = 'manual' WHERE source = 'recipe';
ALTER TABLE pantry_items DROP CONSTRAINT pantry_items_source_check;
ALTER TABLE pantry_items ADD CONSTRAINT pantry_items_source_check CHECK (source IN ('manual', 'capture'));
UPDATE shopping_items SET source = 'manual' WHERE source = 'recipe';
ALTER TABLE shopping_items DROP CONSTRAINT shopping_items_source_check;
ALTER TABLE shopping_items ADD CONSTRAINT shopping_items_source_check CHECK (source IN ('manual', 'capture'));
DROP TABLE IF EXISTS meal_plan_entries;
DROP TABLE IF EXISTS recipe_ingredients;
DROP TABLE IF EXISTS recipes;
