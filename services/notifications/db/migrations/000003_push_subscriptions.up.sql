-- Web push (the webpush channel): the browsers, such as the installed Life OS
-- app on the owner's phone, that turned on notifications. Each subscription
-- is the push service endpoint the browser gave us and the keys its pushes
-- are encrypted for (RFC 8291). Subscribing again with the same endpoint
-- updates the one row.
CREATE TABLE push_subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint TEXT NOT NULL CHECK (endpoint ~ '^https://' AND char_length(endpoint) <= 2048),
    -- The browser's P-256 public key (65 bytes) and auth secret (16 bytes),
    -- base64url without padding.
    p256dh TEXT NOT NULL CHECK (p256dh ~ '^[A-Za-z0-9_-]{87}$'),
    auth TEXT NOT NULL CHECK (auth ~ '^[A-Za-z0-9_-]{22}$'),
    -- Read from the subscribing browser's user agent, such as "Chrome on
    -- Android".
    device_label TEXT NOT NULL CHECK (btrim(device_label) <> '' AND char_length(device_label) <= 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    last_error TEXT,
    CONSTRAINT push_subscriptions_endpoint_key UNIQUE (endpoint)
);

-- A notification's webpush delivery is one row in deliveries, whatever the
-- number of devices. This records each device's final outcome for it, so a
-- retry of the delivery sends only to the devices that have none yet: one
-- device failing for now never sends a notification twice to the others.
CREATE TABLE push_outcomes (
    notification_id UUID NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    subscription_id UUID NOT NULL REFERENCES push_subscriptions (id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('sent', 'failed')),
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (notification_id, subscription_id)
);

CREATE INDEX push_outcomes_subscription_idx ON push_outcomes (subscription_id);
