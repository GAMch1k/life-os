CREATE TABLE example_records (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    label text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
