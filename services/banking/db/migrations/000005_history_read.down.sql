ALTER TABLE bank_connections
    DROP CONSTRAINT bank_connections_history_started,
    DROP COLUMN history_error,
    DROP COLUMN history_transactions,
    DROP COLUMN history_finished_at,
    DROP COLUMN history_started_at,
    DROP COLUMN history_status,
    DROP COLUMN earliest_booking_date;
