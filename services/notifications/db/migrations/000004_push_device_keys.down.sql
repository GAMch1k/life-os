DROP INDEX IF EXISTS push_subscriptions_device_key_idx;
ALTER TABLE push_subscriptions DROP COLUMN IF EXISTS device_key_id;
