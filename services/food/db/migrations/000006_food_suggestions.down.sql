-- Reverses 000006_food_suggestions: drops the ideas, the recipes' methods,
-- and the preferences. Saved ideas stay as the recipes they became.
DROP TABLE IF EXISTS food_ideas;
ALTER TABLE recipes DROP COLUMN IF EXISTS methods;
DROP TABLE IF EXISTS food_preferences;
