package auth

import (
	"net"
	"net/http"

	"cvscreening/be/internal/captcha"
	"cvscreening/be/internal/httpx"
)

type Handler struct {
	svc          *Service
	captcha      *captcha.Verifier
	cookieMaxAge int
	cookieSecure bool
}

func NewHandler(svc *Service, captchaVerifier *captcha.Verifier, cookieMaxAge int, cookieSecure bool) *Handler {
	return &Handler{svc: svc, captcha: captchaVerifier, cookieMaxAge: cookieMaxAge, cookieSecure: cookieSecure}
}

type credentials struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=8,max=72"`
}

type registerRequest struct {
	credentials
	// TurnstileToken is the Cloudflare widget response. Only required/checked
	// when TURNSTILE_SECRET_KEY is configured (captcha.Verifier no-ops otherwise).
	TurnstileToken string `json:"turnstileToken"`
}

type verifyRequest struct {
	Token string `json:"token" validate:"required"`
}

// Register creates a new account (or upgrades a legacy guest cookie in place,
// if one happens to still be present), sets the auth cookie, and returns the
// user + trial balance. The token is NOT in the body — httpOnly cookie only.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var in registerRequest
	if err := httpx.DecodeAndValidate(r, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}

	ip := ClientIP(r)
	ok, err := h.captcha.Verify(in.TurnstileToken, ip)
	if err != nil || !ok {
		httpx.WriteError(w, httpx.ErrCaptchaFailed())
		return
	}

	var guestID *int64
	if idn, ok := IdentityFromContext(r.Context()); ok && idn.IsGuest {
		guestID = &idn.UserID
	}

	user, token, err := h.svc.Register(r.Context(), in.Email, in.Password, ip, guestID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	SetTokenCookie(w, token, h.cookieMaxAge, h.cookieSecure)
	httpx.WriteSuccess(w, http.StatusCreated, map[string]UserView{"user": user})
}

// Login sets the auth cookie and returns the user (no token in the body).
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.DecodeAndValidate(r, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	user, token, err := h.svc.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	SetTokenCookie(w, token, h.cookieMaxAge, h.cookieSecure)
	httpx.WriteSuccess(w, http.StatusOK, map[string]UserView{"user": user})
}

// Logout clears the auth cookie.
func (h *Handler) Logout(w http.ResponseWriter, _ *http.Request) {
	ClearTokenCookie(w, h.cookieSecure)
	httpx.WriteSuccess(w, http.StatusOK, map[string]bool{"loggedOut": true})
}

// Me returns the current account + trial balance. No cookie → 401.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	idn, ok := IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized("Belum ada sesi."))
		return
	}
	user, err := h.svc.Me(r.Context(), idn.UserID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, map[string]UserView{"user": user})
}

// VerifyEmail redeems the token from the confirmation email link and signs
// the user in (see auth.Service.VerifyEmail) — sets the auth cookie so
// clicking the link works from any browser/device, not just the one the
// account was registered from.
func (h *Handler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var in verifyRequest
	if err := httpx.DecodeAndValidate(r, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	user, token, err := h.svc.VerifyEmail(r.Context(), in.Token)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	SetTokenCookie(w, token, h.cookieMaxAge, h.cookieSecure)
	httpx.WriteSuccess(w, http.StatusOK, map[string]UserView{"user": user})
}

// ResendVerification re-sends the confirmation email for the current user.
func (h *Handler) ResendVerification(w http.ResponseWriter, r *http.Request) {
	idn, ok := IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, httpx.ErrUnauthorized("Belum ada sesi."))
		return
	}
	if err := h.svc.ResendVerification(r.Context(), idn.UserID); err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, map[string]bool{"sent": true})
}

// ClientIP extracts the request's IP. middleware.RealIP (router.go) already
// rewrites r.RemoteAddr from trusted proxy headers, so this is just the
// host:port split.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
