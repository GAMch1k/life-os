-- The processor reads the captures made shortly before each capture it plans
-- (GET /v1/recent-captures), a range of creation times over every state.
CREATE INDEX inbox_items_created_idx ON inbox_items (created_at);
