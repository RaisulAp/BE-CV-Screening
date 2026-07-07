package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"cvscreening/be/internal/email"
	"cvscreening/be/internal/httpx"
	"cvscreening/be/internal/store"
)

// bcryptCost is set explicitly (rather than bcrypt.DefaultCost=10) — a couple
// of points of cost is cheap insurance against offline cracking if the DB ever
// leaks, and login-time latency at cost 12 is still imperceptible.
const bcryptCost = 12

const verificationTTL = 24 * time.Hour

type Service struct {
	store       store.Store
	secret      string
	ttl         time.Duration
	email       *email.Client
	frontendURL string
}

func NewService(st store.Store, secret string, ttl time.Duration, emailClient *email.Client, frontendURL string) *Service {
	return &Service{
		store:       st,
		secret:      secret,
		ttl:         ttl,
		email:       emailClient,
		frontendURL: frontendURL,
	}
}

// UserView is what every auth-facing response returns: the account plus its
// current trial balance, so the FE never needs a second round-trip just to
// show "N of 3 trials left" (BILLING_PLAN.md §10.1's "fold into /auth/me").
type UserView struct {
	store.User
	TrialRemaining int `json:"trialRemaining"`
}

func (s *Service) view(ctx context.Context, user store.User) (UserView, error) {
	remaining, err := s.store.GetTrialRemaining(ctx, user.ID)
	if err != nil {
		return UserView{}, err
	}
	return UserView{User: user, TrialRemaining: remaining}, nil
}

// Register creates a real account and returns a token. If guestID is non-nil,
// the existing guest row is upgraded in place so its analyses carry over.
// If email verification is configured (RESEND_API_KEY set), the account starts
// unverified and a confirmation link is emailed; POST /analyses is gated on
// verification (analyses/service.go). If verification isn't configured, the
// account is auto-verified so registration keeps working frictionlessly.
func (s *Service) Register(ctx context.Context, email, password, signupIP string, guestID *int64) (UserView, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	if _, err := s.store.GetUserByEmail(ctx, email); err == nil {
		return UserView{}, "", httpx.ErrEmailTaken()
	} else if !errors.Is(err, store.ErrNotFound) {
		return UserView{}, "", err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return UserView{}, "", err
	}

	params := store.NewUserParams{
		Email:        email,
		PasswordHash: string(hash),
		SignupIP:     signupIP,
	}
	var rawToken string
	if s.email.Enabled() {
		rawToken, err = newVerificationToken()
		if err != nil {
			return UserView{}, "", err
		}
		expires := time.Now().Add(verificationTTL)
		params.VerificationToken = &rawToken
		params.VerificationExpiresAt = &expires
	} else {
		now := time.Now()
		params.EmailVerifiedAt = &now
	}

	var user store.User
	if guestID != nil {
		user, err = s.store.UpgradeGuestToUser(ctx, *guestID, params)
		if errors.Is(err, store.ErrNotFound) {
			// Guest already upgraded / not found — fall back to a fresh account.
			user, err = s.store.CreateUser(ctx, params)
		}
	} else {
		user, err = s.store.CreateUser(ctx, params)
	}
	if err != nil {
		if strings.Contains(err.Error(), "users_email_key") {
			return UserView{}, "", httpx.ErrEmailTaken()
		}
		return UserView{}, "", err
	}

	// Every fresh account gets its 3 lifetime trial credits right away.
	if err := s.store.SeedTrial(ctx, user.ID); err != nil {
		return UserView{}, "", err
	}

	if rawToken != "" {
		// Best-effort: a send failure shouldn't block registration — the user
		// can hit "resend verification email" from the app.
		_ = s.email.SendVerification(user.Email, s.verificationLink(rawToken))
	}

	token, err := s.issue(user.ID, false)
	if err != nil {
		return UserView{}, "", err
	}
	view, err := s.view(ctx, user)
	return view, token, err
}

// Login verifies credentials and returns a token. Same generic error for
// unknown email and wrong password (no account enumeration).
func (s *Service) Login(ctx context.Context, emailAddr, password string) (UserView, string, error) {
	emailAddr = strings.ToLower(strings.TrimSpace(emailAddr))

	user, err := s.store.GetUserByEmail(ctx, emailAddr)
	if errors.Is(err, store.ErrNotFound) {
		return UserView{}, "", httpx.ErrInvalidCredentials()
	} else if err != nil {
		return UserView{}, "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return UserView{}, "", httpx.ErrInvalidCredentials()
	}

	token, err := s.issue(user.ID, false)
	if err != nil {
		return UserView{}, "", err
	}
	view, err := s.view(ctx, user)
	return view, token, err
}

func (s *Service) Me(ctx context.Context, userID int64) (UserView, error) {
	user, err := s.store.GetUserByID(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return UserView{}, httpx.ErrUnauthorized("Sesi tidak valid. Silakan masuk lagi.")
	} else if err != nil {
		return UserView{}, err
	}
	return s.view(ctx, user)
}

// VerifyEmail redeems a verification link token and logs the user in. The
// token itself (a random 256-bit single-use value, only reachable by whoever
// controls the inbox) is treated as sufficient proof of identity — so
// clicking the link from ANY browser/device signs them in directly, instead
// of leaving them "verified" but stuck logged out wherever the link happened
// to be opened.
func (s *Service) VerifyEmail(ctx context.Context, token string) (UserView, string, error) {
	user, err := s.store.GetUserByVerificationToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return UserView{}, "", httpx.ErrInvalidVerificationToken()
	} else if err != nil {
		return UserView{}, "", err
	}
	if err := s.store.VerifyUserEmail(ctx, user.ID); err != nil {
		return UserView{}, "", err
	}
	now := time.Now()
	user.EmailVerifiedAt = &now

	authToken, err := s.issue(user.ID, false)
	if err != nil {
		return UserView{}, "", err
	}
	view, err := s.view(ctx, user)
	return view, authToken, err
}

// ResendVerification issues and emails a fresh token for the current user.
// A no-op (nil error) if already verified or if email sending is disabled.
func (s *Service) ResendVerification(ctx context.Context, userID int64) error {
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.EmailVerifiedAt != nil || !s.email.Enabled() || user.Email == "" {
		return nil
	}
	rawToken, err := newVerificationToken()
	if err != nil {
		return err
	}
	expires := time.Now().Add(verificationTTL)
	if err := s.store.SetVerificationToken(ctx, userID, rawToken, expires); err != nil {
		return err
	}
	return s.email.SendVerification(user.Email, s.verificationLink(rawToken))
}

func (s *Service) issue(userID int64, guest bool) (string, error) {
	return NewToken(s.secret, userID, guest, s.ttl)
}

func (s *Service) verificationLink(token string) string {
	return s.frontendURL + "/verify-email?token=" + token
}

func newVerificationToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
