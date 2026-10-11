-- P15-01 Food: one shopping list grouped by aisle, the stores an item may be
-- tagged with, the aisle rules that place new items, the pantry, and the
-- Idempotency-Key replays of every write.
--
-- Names are kept as written and compared by normalized_name, which the
-- service computes (Unicode NFKC, lower case, ё as е, whitespace collapsed,
-- surrounding punctuation stripped). Actors are X-Life-OS-Actor values such
-- as user, agent:codex-inbox-processor, or dev:claude-code.

-- Stores are archived, never deleted, so items keep pointing at them.
CREATE TABLE stores (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    normalized_name TEXT NOT NULL CHECK (normalized_name <> ''),
    sort_order INTEGER NOT NULL DEFAULT 0,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
);
-- Store names are unique as normalised, so "Lidl" and "LIDL " are one store.
CREATE UNIQUE INDEX stores_normalized_name_key ON stores (normalized_name);

-- aisle_rank orders the list by aisle; it lists the aisles in the order of
-- domain.Aisles.
CREATE TABLE shopping_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    normalized_name TEXT NOT NULL CHECK (normalized_name <> ''),
    amount TEXT,
    note TEXT,
    aisle TEXT NOT NULL CHECK (aisle IN ('produce', 'bakery', 'dairy', 'meat_fish', 'frozen', 'pantry', 'drinks', 'snacks', 'household', 'drugstore', 'other')),
    aisle_rank SMALLINT NOT NULL GENERATED ALWAYS AS (CASE aisle
        WHEN 'produce' THEN 1 WHEN 'bakery' THEN 2 WHEN 'dairy' THEN 3 WHEN 'meat_fish' THEN 4
        WHEN 'frozen' THEN 5 WHEN 'pantry' THEN 6 WHEN 'drinks' THEN 7 WHEN 'snacks' THEN 8
        WHEN 'household' THEN 9 WHEN 'drugstore' THEN 10 ELSE 11 END) STORED,
    -- rule: the aisle rules chose the aisle (they choose again on a rename);
    -- manual: a request named it.
    aisle_set_by TEXT NOT NULL CHECK (aisle_set_by IN ('rule', 'manual')),
    store_id UUID REFERENCES stores (id),
    state TEXT NOT NULL DEFAULT 'needed' CHECK (state IN ('needed', 'bought')),
    bought_at TIMESTAMPTZ,
    source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'capture')),
    source_ref TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    CHECK ((state = 'bought') = (bought_at IS NOT NULL))
);
-- At most one open item per normalised name: adding a name that is already
-- open merges into that item (an upsert on this index), also when two adds
-- race.
CREATE UNIQUE INDEX shopping_items_open_name_key ON shopping_items (normalized_name) WHERE state = 'needed';
CREATE INDEX shopping_items_open_order_idx ON shopping_items (aisle_rank, normalized_name, id) WHERE state = 'needed';
CREATE INDEX shopping_items_bought_order_idx ON shopping_items (bought_at DESC, id DESC) WHERE state = 'bought';
CREATE INDEX shopping_items_store_idx ON shopping_items (store_id) WHERE store_id IS NOT NULL;

-- A rule places a new item whose normalised name contains keyword (itself
-- normalised) in aisle: as whole words (word) or anywhere, also inside a
-- word (substring, for German compounds). The longest matching keyword wins.
CREATE TABLE aisle_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    keyword TEXT NOT NULL CHECK (keyword <> ''),
    aisle TEXT NOT NULL CHECK (aisle IN ('produce', 'bakery', 'dairy', 'meat_fish', 'frozen', 'pantry', 'drinks', 'snacks', 'household', 'drugstore', 'other')),
    match TEXT NOT NULL DEFAULT 'word' CHECK (match IN ('word', 'substring')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL
);
CREATE UNIQUE INDEX aisle_rules_keyword_key ON aisle_rules (keyword);

-- Pantry items are kept when they end (used up or thrown away), for history.
-- from_shopping_item_id names the shopping item it was bought as; it is not a
-- foreign key because bought items are cleared from the list.
CREATE TABLE pantry_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    normalized_name TEXT NOT NULL CHECK (normalized_name <> ''),
    amount TEXT,
    location TEXT NOT NULL CHECK (location IN ('fridge', 'freezer', 'pantry', 'other')),
    expires_on DATE,
    opened_on DATE,
    note TEXT,
    state TEXT NOT NULL DEFAULT 'in_stock' CHECK (state IN ('in_stock', 'used_up', 'thrown_away')),
    ended_at TIMESTAMPTZ,
    source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'capture')),
    source_ref TEXT,
    from_shopping_item_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    CHECK ((state = 'in_stock') = (ended_at IS NULL))
);
CREATE INDEX pantry_items_stock_order_idx ON pantry_items (COALESCE(expires_on, 'infinity'::date), normalized_name, id) WHERE state = 'in_stock';
CREATE INDEX pantry_items_ended_order_idx ON pantry_items (ended_at DESC, id DESC) WHERE state <> 'in_stock';

-- A write sent with an Idempotency-Key stores its response here in the same
-- transaction. A repeat with the same request (method, path, and body) gets
-- that response again; the same key with another request is refused.
CREATE TABLE idempotency_keys (
    key TEXT PRIMARY KEY,
    request_hash TEXT NOT NULL,
    status INTEGER NOT NULL DEFAULT 0,
    response BYTEA NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);

-- The built-in aisle rules: common grocery, household, and drugstore words in
-- English and Russian (whole words) and German (substrings, so compounds such
-- as "Vollmilch" or "Olivenöl" match). Keywords are normalised; a few longer
-- ones exist so the longest match picks the right aisle ("Orangensaft" is a
-- drink, not produce; "масло подсолнечное" is oil, not butter). The owner
-- can delete any of them and add their own.
INSERT INTO aisle_rules (keyword, aisle, match, created_by)
SELECT keyword, aisle, match, 'system:food' FROM (VALUES
    -- produce
    ('apples', 'produce', 'word'), ('bananas', 'produce', 'word'), ('tomatoes', 'produce', 'word'),
    ('potatoes', 'produce', 'word'), ('onions', 'produce', 'word'), ('cucumber', 'produce', 'word'),
    ('lettuce', 'produce', 'word'), ('avocado', 'produce', 'word'),
    ('яблоки', 'produce', 'word'), ('бананы', 'produce', 'word'), ('помидоры', 'produce', 'word'),
    ('огурцы', 'produce', 'word'), ('картошка', 'produce', 'word'), ('картофель', 'produce', 'word'),
    ('лук', 'produce', 'word'), ('морковь', 'produce', 'word'), ('перец', 'produce', 'word'),
    ('apfel', 'produce', 'substring'), ('äpfel', 'produce', 'substring'), ('banane', 'produce', 'substring'),
    ('tomate', 'produce', 'substring'), ('kartoffel', 'produce', 'substring'), ('zwiebel', 'produce', 'substring'),
    ('gurke', 'produce', 'substring'), ('salat', 'produce', 'substring'), ('paprika', 'produce', 'substring'),
    ('möhre', 'produce', 'substring'), ('karotte', 'produce', 'substring'), ('beeren', 'produce', 'substring'),
    ('trauben', 'produce', 'substring'), ('wassermelone', 'produce', 'substring'),
    -- bakery
    ('bread', 'bakery', 'word'), ('rolls', 'bakery', 'word'), ('toast', 'bakery', 'word'),
    ('хлеб', 'bakery', 'word'), ('батон', 'bakery', 'word'), ('булочки', 'bakery', 'word'),
    ('brot', 'bakery', 'substring'), ('brötchen', 'bakery', 'substring'), ('semmel', 'bakery', 'substring'),
    ('brezel', 'bakery', 'substring'), ('kuchen', 'bakery', 'substring'), ('baguette', 'bakery', 'substring'),
    ('croissant', 'bakery', 'substring'),
    -- dairy and eggs
    ('milk', 'dairy', 'word'), ('butter', 'dairy', 'word'), ('cheese', 'dairy', 'word'),
    ('yogurt', 'dairy', 'word'), ('cream', 'dairy', 'word'), ('eggs', 'dairy', 'word'),
    ('молоко', 'dairy', 'word'), ('кефир', 'dairy', 'word'), ('сметана', 'dairy', 'word'),
    ('творог', 'dairy', 'word'), ('сыр', 'dairy', 'word'), ('масло', 'dairy', 'word'),
    ('сливки', 'dairy', 'word'), ('йогурт', 'dairy', 'word'), ('яйца', 'dairy', 'word'),
    ('milch', 'dairy', 'substring'), ('käse', 'dairy', 'substring'), ('joghurt', 'dairy', 'substring'),
    ('quark', 'dairy', 'substring'), ('sahne', 'dairy', 'substring'), ('eier', 'dairy', 'substring'),
    -- meat and fish
    ('chicken', 'meat_fish', 'word'), ('beef', 'meat_fish', 'word'), ('pork', 'meat_fish', 'word'),
    ('ham', 'meat_fish', 'word'), ('bacon', 'meat_fish', 'word'), ('sausages', 'meat_fish', 'word'),
    ('fish', 'meat_fish', 'word'), ('salmon', 'meat_fish', 'word'),
    ('мясо', 'meat_fish', 'word'), ('курица', 'meat_fish', 'word'), ('фарш', 'meat_fish', 'word'),
    ('колбаса', 'meat_fish', 'word'), ('сосиски', 'meat_fish', 'word'), ('рыба', 'meat_fish', 'word'),
    ('fleisch', 'meat_fish', 'substring'), ('hähnchen', 'meat_fish', 'substring'), ('schwein', 'meat_fish', 'substring'),
    ('wurst', 'meat_fish', 'substring'), ('schinken', 'meat_fish', 'substring'), ('speck', 'meat_fish', 'substring'),
    ('salami', 'meat_fish', 'substring'), ('fisch', 'meat_fish', 'substring'), ('lachs', 'meat_fish', 'substring'),
    -- frozen
    ('ice cream', 'frozen', 'word'), ('frozen', 'frozen', 'word'),
    ('мороженое', 'frozen', 'word'), ('пельмени', 'frozen', 'word'),
    ('eis', 'frozen', 'word'), ('speiseeis', 'frozen', 'substring'), ('tiefkühl', 'frozen', 'substring'),
    ('fischstäbchen', 'frozen', 'substring'),
    -- pantry
    ('rice', 'pantry', 'word'), ('pasta', 'pantry', 'word'), ('flour', 'pantry', 'word'),
    ('sugar', 'pantry', 'word'), ('salt', 'pantry', 'word'), ('oil', 'pantry', 'word'),
    ('honey', 'pantry', 'word'), ('coffee', 'pantry', 'word'), ('tea', 'pantry', 'word'),
    ('рис', 'pantry', 'word'), ('гречка', 'pantry', 'word'), ('макароны', 'pantry', 'word'),
    ('мука', 'pantry', 'word'), ('сахар', 'pantry', 'word'), ('соль', 'pantry', 'word'),
    ('подсолнечное', 'pantry', 'word'), ('растительное', 'pantry', 'word'), ('оливковое', 'pantry', 'word'),
    ('мед', 'pantry', 'word'), ('кофе', 'pantry', 'word'), ('чай', 'pantry', 'word'),
    ('reis', 'pantry', 'substring'), ('nudeln', 'pantry', 'substring'), ('mehl', 'pantry', 'substring'),
    ('zucker', 'pantry', 'substring'), ('salz', 'pantry', 'substring'), ('öl', 'pantry', 'substring'),
    ('essig', 'pantry', 'substring'), ('honig', 'pantry', 'substring'), ('kaffee', 'pantry', 'substring'),
    ('tee', 'pantry', 'substring'), ('müsli', 'pantry', 'substring'), ('tomatenmark', 'pantry', 'substring'),
    -- drinks
    ('water', 'drinks', 'word'), ('juice', 'drinks', 'word'), ('beer', 'drinks', 'word'),
    ('wine', 'drinks', 'word'), ('cola', 'drinks', 'word'),
    ('вода', 'drinks', 'word'), ('сок', 'drinks', 'word'), ('пиво', 'drinks', 'word'), ('вино', 'drinks', 'word'),
    ('wasser', 'drinks', 'substring'), ('sprudel', 'drinks', 'substring'), ('saft', 'drinks', 'substring'),
    ('orangensaft', 'drinks', 'substring'), ('apfelsaft', 'drinks', 'substring'), ('bier', 'drinks', 'substring'),
    ('wein', 'drinks', 'substring'), ('schorle', 'drinks', 'substring'), ('eistee', 'drinks', 'substring'),
    -- snacks
    ('chocolate', 'snacks', 'word'), ('cookies', 'snacks', 'word'), ('crisps', 'snacks', 'word'), ('nuts', 'snacks', 'word'),
    ('чипсы', 'snacks', 'word'), ('шоколад', 'snacks', 'word'), ('печенье', 'snacks', 'word'), ('конфеты', 'snacks', 'word'),
    ('chips', 'snacks', 'substring'), ('schokolade', 'snacks', 'substring'), ('kekse', 'snacks', 'substring'),
    ('nüsse', 'snacks', 'substring'), ('kartoffelchips', 'snacks', 'substring'), ('salzstangen', 'snacks', 'substring'),
    -- household
    ('toilet paper', 'household', 'word'), ('paper towels', 'household', 'word'), ('dish soap', 'household', 'word'),
    ('detergent', 'household', 'word'), ('trash bags', 'household', 'word'),
    ('туалетная бумага', 'household', 'word'), ('посуды', 'household', 'word'), ('порошок', 'household', 'word'),
    ('мусора', 'household', 'word'),
    ('toilettenpapier', 'household', 'substring'), ('klopapier', 'household', 'substring'), ('küchenrolle', 'household', 'substring'),
    ('spülmittel', 'household', 'substring'), ('waschmittel', 'household', 'substring'), ('müllbeutel', 'household', 'substring'),
    ('alufolie', 'household', 'substring'),
    -- drugstore
    ('soap', 'drugstore', 'word'), ('shower gel', 'drugstore', 'word'), ('toothpaste', 'drugstore', 'word'),
    ('toothbrush', 'drugstore', 'word'), ('deodorant', 'drugstore', 'word'),
    ('шампунь', 'drugstore', 'word'), ('мыло', 'drugstore', 'word'), ('для душа', 'drugstore', 'word'),
    ('зубная паста', 'drugstore', 'word'), ('дезодорант', 'drugstore', 'word'),
    ('shampoo', 'drugstore', 'substring'), ('duschgel', 'drugstore', 'substring'), ('seife', 'drugstore', 'substring'),
    ('zahnpasta', 'drugstore', 'substring'), ('zahnbürste', 'drugstore', 'substring'), ('deo', 'drugstore', 'substring'),
    ('windeln', 'drugstore', 'substring')
) AS seed (keyword, aisle, match);
