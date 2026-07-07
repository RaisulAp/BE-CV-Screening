package auth

import "net/http"

// CookieName holds the JWT. HttpOnly so JS can't read it; the browser attaches
// it automatically to every request (BACKEND.md — cookie-based auth).
const CookieName = "token"

// SetTokenCookie writes the auth cookie. maxAge is in seconds. Secure should be
// true in production (HTTPS); false is fine for localhost dev.
func SetTokenCookie(w http.ResponseWriter, token string, maxAge int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearTokenCookie expires the auth cookie (logout).
func ClearTokenCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
