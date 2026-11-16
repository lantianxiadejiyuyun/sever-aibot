-- 0003_step_up.sql — short-lived re-authentication for credential reveal.
ALTER TABLE admin_sessions ADD COLUMN step_up_until TEXT;
