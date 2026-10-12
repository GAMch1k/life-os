-- P13-09 AI budget plans: the owner (or the monthly schedule) asks for a
-- plan for a month, the processor's job leaves the AI's proposal, and the
-- owner applies it to the month's budget or dismisses it. Nothing changes
-- the budget until the owner applies a plan. Existing rows are unchanged.
--
-- state: requested (waiting for the job, maybe until retry_at), running
-- (claimed by the job at claimed_at), ready (result holds the proposal),
-- failed, applied or partly_applied (applied holds what the owner applied),
-- and dismissed.

CREATE TABLE ai_budget_plans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    month DATE NOT NULL CHECK (month = date_trunc('month', month)::date),
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    -- The owner's savings goal for the month, when they gave one.
    savings_goal_minor BIGINT CHECK (savings_goal_minor >= 0),
    trigger TEXT NOT NULL CHECK (trigger IN ('request', 'schedule')),
    state TEXT NOT NULL DEFAULT 'requested'
        CHECK (state IN ('requested', 'running', 'ready', 'failed', 'applied', 'partly_applied', 'dismissed')),
    requested_by_kind TEXT NOT NULL CHECK (requested_by_kind IN ('user', 'agent', 'system', 'dev')),
    requested_by_name TEXT,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    claimed_at TIMESTAMPTZ,
    retry_at TIMESTAMPTZ,
    error TEXT,
    result JSONB,
    finished_at TIMESTAMPTZ,
    applied JSONB,
    applied_at TIMESTAMPTZ,
    dismissed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A plan that is ready has its proposal, and an applied one what was
    -- applied.
    CHECK (state NOT IN ('ready', 'applied', 'partly_applied') OR result IS NOT NULL),
    CHECK (state NOT IN ('applied', 'partly_applied') OR applied IS NOT NULL)
);

-- One scheduled plan per month: a restarted processor that asks again gets
-- the same one.
CREATE UNIQUE INDEX ai_budget_plans_schedule_idx ON ai_budget_plans (month) WHERE trigger = 'schedule';
CREATE INDEX ai_budget_plans_month_idx ON ai_budget_plans (month, created_at DESC);
-- What the job may claim.
CREATE INDEX ai_budget_plans_waiting_idx ON ai_budget_plans (created_at) WHERE state IN ('requested', 'running');
