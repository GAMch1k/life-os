-- Rolling back forgets the payday setting and which payment asked for a
-- plan: a salary plan stays as a plan that was asked for (trigger request).
-- The claims of the ready notifications stay, so migrating forward again
-- sends none twice.

DROP INDEX IF EXISTS ai_budget_plans_salary_transaction_idx;
ALTER TABLE ai_budget_plans DROP CONSTRAINT IF EXISTS ai_budget_plans_salary_check;
ALTER TABLE ai_budget_plans DROP COLUMN IF EXISTS salary_booked_on, DROP COLUMN IF EXISTS salary_transaction_id;
UPDATE ai_budget_plans SET trigger = 'request' WHERE trigger = 'salary';
ALTER TABLE ai_budget_plans DROP CONSTRAINT ai_budget_plans_trigger_check;
ALTER TABLE ai_budget_plans ADD CONSTRAINT ai_budget_plans_trigger_check CHECK (trigger IN ('request', 'schedule'));

DROP TABLE IF EXISTS payday_settings;
