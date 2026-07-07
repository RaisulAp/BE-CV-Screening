-- Login-gated trial quota: guest analysis is removed entirely (auth pivot —
-- every analysis now requires a real, logged-in account). Replaces the old
-- guest 3x-per-IP limit and the registered 20/month rolling quota with a
-- single per-account trial bucket of 3 lifetime credits.
--
-- This is deliberately a trimmed slice of the `user_billing` table designed
-- in Progress/BILLING_PLAN.md §5.1 ("Fase 1 — Fondasi kuota, tanpa uang") —
-- only the trial_remaining bucket, none of the future subscription/top-up
-- columns (plan, paid_until, daily_*, topup_balance) or payment tables
-- (orders, plans, topup_packages, billing_settings). Those arrive additively
-- in a later migration if/when the payments work in that doc is built.

CREATE TABLE user_billing (
    user_id         BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    trial_remaining INT NOT NULL DEFAULT 3,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_trial_nonneg CHECK (trial_remaining >= 0)
);

-- Backfill existing registered users, discounting analyses they already ran
-- under the old (near-unlimited) monthly quota — fair, and mirrors the
-- guest-upgrade discount formula from BILLING_PLAN.md §14/§11.
INSERT INTO user_billing (user_id, trial_remaining)
SELECT u.id, GREATEST(0, 3 - COALESCE(c.n, 0))
FROM users u
LEFT JOIN (
    SELECT user_id, count(*) n FROM analyses
    WHERE parent_analysis_id IS NULL GROUP BY user_id
) c ON c.user_id = u.id
WHERE u.is_guest = false
ON CONFLICT (user_id) DO NOTHING;
