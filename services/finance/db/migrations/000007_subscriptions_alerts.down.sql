-- Rolling back removes the subscriptions, the alert settings, and the alert
-- claims and low-balance episodes; every transaction, account, and category
-- stays as it is.

DROP TABLE IF EXISTS low_balance_states;
DROP TABLE IF EXISTS alert_notifications;
DROP TABLE IF EXISTS finance_settings;
DROP TABLE IF EXISTS subscription_transactions;
DROP TABLE IF EXISTS subscriptions;
