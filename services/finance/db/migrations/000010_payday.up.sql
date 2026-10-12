-- P13-10 payday: the AI plans the budget when the salary arrives. The owner
-- names the payer (matched like a rule matches a counterparty) and an
-- optional minimum amount; a booked payment from them asks for a plan with
-- trigger salary, which remembers the payment. Existing rows are unchanged.

-- One row: the payday setting, off until the owner saves a payer. The
-- minimum is in minor units of the default currency.
CREATE TABLE payday_settings (
    id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    enabled BOOLEAN NOT NULL DEFAULT false,
    payer_match TEXT CHECK (btrim(payer_match) <> '' AND char_length(payer_match) <= 200),
    min_amount_minor BIGINT CHECK (min_amount_minor > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (NOT enabled OR payer_match IS NOT NULL)
);
INSERT INTO payday_settings DEFAULT VALUES;

-- A salary plan names the payment that asked for it (null once that
-- transaction is deleted) and the day it was booked; a payment asks once.
ALTER TABLE ai_budget_plans DROP CONSTRAINT ai_budget_plans_trigger_check;
ALTER TABLE ai_budget_plans ADD CONSTRAINT ai_budget_plans_trigger_check CHECK (trigger IN ('request', 'schedule', 'salary'));
ALTER TABLE ai_budget_plans
    ADD COLUMN salary_transaction_id UUID REFERENCES transactions(id) ON DELETE SET NULL,
    ADD COLUMN salary_booked_on DATE,
    ADD CONSTRAINT ai_budget_plans_salary_check CHECK ((trigger = 'salary') = (salary_booked_on IS NOT NULL));
CREATE UNIQUE INDEX ai_budget_plans_salary_transaction_idx ON ai_budget_plans (salary_transaction_id) WHERE salary_transaction_id IS NOT NULL;
