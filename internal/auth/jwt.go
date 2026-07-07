package auth

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type ctxKey struct{}

var identityKey = ctxKey{}

// Identity is the resolved caller — either a guest session or a real account.
type Identity struct {
	UserID  int64
	IsGuest bool
}

// Claims embeds the standard registered claims plus a guest flag.
type Claims struct {
	Guest bool `json:"guest"`
	jwt.RegisteredClaims
}

// NewToken issues a signed JWT whose subject is the (integer) user id.
func NewToken(secret string, userID int64, guest bool, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		Guest: guest,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// ParseToken validates the signature/expiry and returns the identity.
func ParseToken(secret, tokenStr string) (Identity, error) {
	var claims Claims
	tok, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !tok.Valid {
		return Identity{}, errors.New("invalid token")
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		return Identity{}, errors.New("token missing subject")
	}
	return Identity{UserID: userID, IsGuest: claims.Guest}, nil
}

// WithIdentity stores the resolved identity on the request context.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// IdentityFromContext reads the identity set by the withIdentity middleware.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}
