-- Without delivery, reminders are stored intent only again. Events and their
-- reminders are kept.
DROP TABLE IF EXISTS calendar_reminder_log;
DROP TABLE IF EXISTS calendar_reminder_dispatch;
