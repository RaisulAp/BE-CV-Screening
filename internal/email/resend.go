// Package email sends transactional email via Resend (https://resend.com).
package email

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client sends verification emails. A zero-value apiKey makes every Send a
// no-op — verification is optional until RESEND_API_KEY is configured
// (BACKEND.md-style graceful degrade, matching the AI_MOCK precedent).
type Client struct {
	apiKey string
	from   string
	http   *http.Client
}

func NewClient(apiKey, from string) *Client {
	return &Client{apiKey: apiKey, from: from, http: &http.Client{Timeout: 10 * time.Second}}
}

// Enabled reports whether an API key is configured.
func (c *Client) Enabled() bool { return c.apiKey != "" }

// SendVerification emails a link the user clicks to confirm ownership of
// their address. No-op (nil error) if the client isn't configured.
func (c *Client) SendVerification(to, verifyURL string) error {
	if !c.Enabled() {
		return nil
	}

	html := fmt.Sprintf(`
		<div style="font-family:sans-serif;max-width:480px;margin:0 auto">
			<h2 style="color:#0D9488">Verifikasi email kamu</h2>
			<p>Klik tombol di bawah untuk mengaktifkan akun Teman Melamar Kerja-mu:</p>
			<p><a href="%s" style="display:inline-block;background:#F97316;color:#fff;padding:12px 24px;border-radius:8px;text-decoration:none;font-weight:600">Verifikasi Email</a></p>
			<p style="color:#6b7280;font-size:13px">Atau salin link ini: %s</p>
			<p style="color:#6b7280;font-size:13px">Link berlaku 24 jam. Kalau kamu tidak merasa mendaftar, abaikan email ini.</p>
		</div>`, verifyURL, verifyURL)

	body, err := json.Marshal(map[string]any{
		"from":    c.from,
		"to":      []string{to},
		"subject": "Verifikasi email — Teman Melamar Kerja",
		"html":    html,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("resend: status %d", resp.StatusCode)
	}
	return nil
}
