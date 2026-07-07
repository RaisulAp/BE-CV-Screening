package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const userCols = `id, COALESCE(email, ''), COALESCE(password_hash, ''), is_guest, email_verified_at, created_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.IsGuest, &u.EmailVerifiedAt, &u.CreatedAt)
	return u, err
}

func (s *PgStore) CreateUser(ctx context.Context, p NewUserParams) (User, error) {
	return scanUser(s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, is_guest, signup_ip, verification_token, verification_expires_at, email_verified_at)
		 VALUES ($1, $2, false, $3, $4, $5, $6)
		 RETURNING `+userCols,
		p.Email, p.PasswordHash, p.SignupIP, p.VerificationToken, p.VerificationExpiresAt, p.EmailVerifiedAt))
}

// UpgradeGuestToUser turns an existing guest row into a real account, keeping
// its id (and therefore all analyses created as a guest).
func (s *PgStore) UpgradeGuestToUser(ctx context.Context, guestID int64, p NewUserParams) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`UPDATE users SET email = $2, password_hash = $3, is_guest = false,
		        verification_token = $4, verification_expires_at = $5, email_verified_at = $6
		 WHERE id = $1 AND is_guest = true
		 RETURNING `+userCols, guestID, p.Email, p.PasswordHash, p.VerificationToken, p.VerificationExpiresAt, p.EmailVerifiedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *PgStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE email = $1`, email))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *PgStore) GetUserByID(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// GetUserByVerificationToken looks up a not-yet-expired verification token.
// Expired or unknown tokens both come back as ErrNotFound (no distinction
// leaked to the caller — same handling either way: ask them to resend).
func (s *PgStore) GetUserByVerificationToken(ctx context.Context, token string) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userCols+` FROM users
		 WHERE verification_token = $1 AND verification_expires_at > now()`, token))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// VerifyUserEmail marks the account verified and burns the token (single use).
func (s *PgStore) VerifyUserEmail(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET email_verified_at = now(), verification_token = NULL, verification_expires_at = NULL
		 WHERE id = $1`, userID)
	return err
}

// SetVerificationToken issues a fresh token (used by "resend verification email").
func (s *PgStore) SetVerificationToken(ctx context.Context, userID int64, token string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET verification_token = $2, verification_expires_at = $3 WHERE id = $1`,
		userID, token, expiresAt)
	return err
}
