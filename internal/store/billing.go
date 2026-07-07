package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrTrialExhausted is returned by ConsumeTrial when the account has no
// trial credits left.
var ErrTrialExhausted = errors.New("store: trial exhausted")

// SeedTrial gives a freshly-registered account its 3 lifetime trial credits
// (column default). Idempotent — safe to call even if a row already exists
// (e.g. a guest-upgrade edge case), since it never overwrites an existing row.
func (s *PgStore) SeedTrial(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_billing (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, userID)
	return err
}

// GetTrialRemaining reads the current balance, for surfacing in GET /auth/me.
func (s *PgStore) GetTrialRemaining(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT trial_remaining FROM user_billing WHERE user_id = $1`, userID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return n, err
}

// ConsumeTrial atomically decrements one credit. The WHERE clause makes this
// safe under concurrent requests without an explicit transaction/SELECT FOR
// UPDATE: Postgres row-level locking on UPDATE serializes concurrent
// decrements of the same row, and the affected-row check (zero rows → no
// balance) turns "already at zero" into ErrTrialExhausted instead of ever
// going negative. Simpler than a multi-bucket priority scheme because there's
// only one bucket today (see BILLING_PLAN.md §6's own note on this trade-off).
func (s *PgStore) ConsumeTrial(ctx context.Context, userID int64) (int, error) {
	var remaining int
	err := s.pool.QueryRow(ctx,
		`UPDATE user_billing SET trial_remaining = trial_remaining - 1, updated_at = now()
		 WHERE user_id = $1 AND trial_remaining > 0
		 RETURNING trial_remaining`, userID).Scan(&remaining)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrTrialExhausted
	}
	return remaining, err
}
