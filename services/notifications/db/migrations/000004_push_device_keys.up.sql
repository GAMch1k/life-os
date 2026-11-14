-- P21-06 (docs/api/devices.md#push-notifications-and-the-key): a phone or
-- watch paired with an account subscribes with its device key, and its
-- subscription remembers the key's id, so that revoking the key (in the
-- Dashboard, auth_db) deletes the subscription too. A browser's (the TWA's)
-- has none. P21-07 adds the user.
ALTER TABLE push_subscriptions ADD COLUMN device_key_id UUID;

CREATE INDEX push_subscriptions_device_key_idx
  ON push_subscriptions (device_key_id) WHERE device_key_id IS NOT NULL;
