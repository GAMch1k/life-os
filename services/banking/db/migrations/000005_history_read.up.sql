-- The history read (P13-07). PSD2 lets a bank serve transactions older than
-- 90 days only with fresh strong customer authentication, so right after
-- the owner logged in at the bank (a connect or reconnect) Banking reads up
-- to BANKING_HISTORY_DAYS back in the background and imports what is older
-- than the normal sync's window into Finance.
ALTER TABLE bank_connections
    -- The earliest booking date of a booked transaction Finance accepted
    -- from this connection (normal syncs and history reads): the Dashboard's
    -- "Transactions since".
    ADD COLUMN earliest_booking_date DATE,
    -- The last history read: running, completed, or failed (history_error
    -- says why, in plain words; a failure never touches the connection's
    -- status or its normal syncs), when it started and finished, and how
    -- many older transactions Finance accepted from it.
    ADD COLUMN history_status TEXT CHECK (history_status IN ('running', 'completed', 'failed')),
    ADD COLUMN history_started_at TIMESTAMPTZ,
    ADD COLUMN history_finished_at TIMESTAMPTZ,
    ADD COLUMN history_transactions INTEGER CHECK (history_transactions >= 0),
    ADD COLUMN history_error TEXT,
    ADD CONSTRAINT bank_connections_history_started CHECK ((history_status IS NULL) = (history_started_at IS NULL));
