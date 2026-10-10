-- P13-04 monthly budgets: a system key for the default categories Finance
-- adds (kept when the owner renames one), a category chosen from a card's
-- merchant category code (categorized_by 'mcc'), budget plans (a regular
-- plan that rolls forward from its first month, and months with their own
-- plan) with an amount per category, the budget notifications already sent,
-- and the one-time seeds Finance has applied. Existing rows are unchanged.

ALTER TABLE financial_categories
    ADD COLUMN system_key TEXT CHECK (system_key ~ '^[a-z_]{1,40}$');
ALTER TABLE financial_categories
    ADD CONSTRAINT financial_categories_system_key_unique UNIQUE (system_key);

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_categorized_by_check;
ALTER TABLE transactions
    ADD CONSTRAINT transactions_categorized_by_check CHECK (categorized_by IN ('manual', 'rule', 'mcc'));

-- scope 'regular': the plan of its month and every later month, until a
-- later regular plan starts. scope 'month': only its month's plan.
CREATE TABLE budget_plans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    month DATE NOT NULL CHECK (month = date_trunc('month', month)::date),
    scope TEXT NOT NULL CHECK (scope IN ('regular', 'month')),
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    expected_income_minor BIGINT NOT NULL DEFAULT 0 CHECK (expected_income_minor >= 0),
    savings_goal_minor BIGINT NOT NULL DEFAULT 0 CHECK (savings_goal_minor >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (month, scope)
);

CREATE TABLE budget_plan_lines (
    plan_id UUID NOT NULL REFERENCES budget_plans(id) ON DELETE CASCADE,
    category_id UUID NOT NULL REFERENCES financial_categories(id) ON DELETE CASCADE,
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    PRIMARY KEY (plan_id, category_id)
);

-- A budget notification is claimed here before it is sent, so each is sent
-- once across restarts; a send that fails for now gives the claim back.
CREATE TABLE budget_notifications (
    dedupe_key TEXT PRIMARY KEY,
    month DATE NOT NULL,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE finance_seeds (
    name TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX transactions_month_currency_idx ON transactions (currency, occurred_on);
