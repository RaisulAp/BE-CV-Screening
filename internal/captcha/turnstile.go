// Package captcha verifies Cloudflare Turnstile tokens server-side, guarding
// registration against scripted/bulk account creation.
package captcha

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

const verifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Verifier checks Turnstile tokens. A zero-value secretKey makes Verify always
// pass — CAPTCHA is optional until TURNSTILE_SECRET_KEY is configured.
type Verifier struct {
	secretKey string
	http      *http.Client
}

func NewVerifier(secretKey string) *Verifier {
	return &Verifier{secretKey: secretKey, http: &http.Client{Timeout: 10 * time.Second}}
}

// Enabled reports whether a secret key is configured.
func (v *Verifier) Enabled() bool { return v.secretKey != "" }

// Verify checks a widget response token against Cloudflare. If the verifier
// isn't configured, it always succeeds (nil error).
func (v *Verifier) Verify(token, remoteIP string) (bool, error) {
	if !v.Enabled() {
		return true, nil
	}
	if token == "" {
		return false, nil
	}

	resp, err := v.http.PostForm(verifyURL, url.Values{
		"secret":   {v.secretKey},
		"response": {token},
		"remoteip": {remoteIP},
	})
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var result struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.Success, nil
}
