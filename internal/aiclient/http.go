package aiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// Sentinel errors the pipeline maps to fail_reason codes (BACKEND.md §7).
var (
	ErrUnavailable  = errors.New("ai service unavailable")
	ErrCVUnreadable = errors.New("cv unreadable")
	ErrBadOutput    = errors.New("ai bad output")
)

// Per-endpoint timeouts — LLM lokal lambat, so these are generous (§7).
var timeouts = map[string]time.Duration{
	"/analyze/jd": 90 * time.Second,
	"/parse/cv":   120 * time.Second,
	"/match":      120 * time.Second,
	"/rewrite":    60 * time.Second,
}

type HTTPClient struct {
	baseURL string
	http    *http.Client
}

func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{
		baseURL: baseURL,
		// No client-level timeout; we bound each call via context instead.
		http: &http.Client{},
	}
}

func (c *HTTPClient) AnalyzeJD(ctx context.Context, text string) (JDResult, error) {
	var out JDResult
	err := c.postJSON(ctx, "/analyze/jd", map[string]string{"text": text}, &out)
	return out, err
}

func (c *HTTPClient) Match(ctx context.Context, jd JDResult, cv CVResult) (MatchResult, error) {
	var out MatchResult
	err := c.postJSON(ctx, "/match", map[string]any{"jd_json": jd, "cv_json": cv}, &out)
	return out, err
}

func (c *HTTPClient) Rewrite(ctx context.Context, bullet, jdContext string) (RewriteResult, error) {
	var out RewriteResult
	err := c.postJSON(ctx, "/rewrite", map[string]string{"bullet": bullet, "jd_context": jdContext}, &out)
	return out, err
}

func (c *HTTPClient) ParseCV(ctx context.Context, fileName string, data []byte) (CVResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeouts["/parse/cv"])
	defer cancel()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", fileName)
	if err != nil {
		return CVResult{}, err
	}
	if _, err := fw.Write(data); err != nil {
		return CVResult{}, err
	}
	if err := mw.Close(); err != nil {
		return CVResult{}, err
	}

	var out CVResult
	err = c.doWithRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/parse/cv", bytes.NewReader(body.Bytes()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		return c.send(req, &out)
	})
	return out, err
}

func (c *HTTPClient) postJSON(ctx context.Context, path string, payload any, out any) error {
	timeout := timeouts[path]
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.doWithRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		return c.send(req, out)
	})
}

// doWithRetry retries once, and only for connection-level failures (AI service
// not up yet). Logical 4xx/5xx are not retried (§7).
func (c *HTTPClient) doWithRetry(ctx context.Context, fn func() error) error {
	err := fn()
	if errors.Is(err, ErrUnavailable) && ctx.Err() == nil {
		time.Sleep(500 * time.Millisecond)
		return fn()
	}
	return err
}

func (c *HTTPClient) send(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnprocessableEntity {
		return ErrCVUnreadable
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%w: status %d: %s", ErrBadOutput, resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrBadOutput, err)
	}
	return nil
}
