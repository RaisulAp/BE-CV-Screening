// Package demo exposes a handful of no-login, no-persistence endpoints that
// talk to the AI Service directly and synchronously. They exist purely so the
// product can be tried/demoed end-to-end (e.g. showing a friend, or poking at
// the AI Service from Postman) without going through the FE, auth, or the
// database. Nothing here reads or writes the store.
//
// Gated behind DEMO_ENABLED (default off) — see internal/config and
// internal/server/router.go. Keep it off outside local/dev use: these routes
// let anyone who can reach the BE run the (slow, real) AI Service for free.
package demo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"cvscreening/be/internal/aiclient"
	"cvscreening/be/internal/httpx"
)

const (
	minJDChars = 100
	maxJDChars = 15000
)

type Handler struct {
	ai         aiclient.Client
	maxCVBytes int64
}

func NewHandler(ai aiclient.Client, maxCVBytes int64) *Handler {
	return &Handler{ai: ai, maxCVBytes: maxCVBytes}
}

// Routes mounts the demo endpoints. No identity/auth check anywhere in this
// package on purpose — see package doc.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/demo/analyze-jd", h.analyzeJD)
	r.Post("/demo/parse-cv", h.parseCV)
	r.Post("/demo/match", h.match)
}

// analyzeJD: POST /demo/analyze-jd  {"text": "..."}  -> aiclient.JDResult
func (h *Handler) analyzeJD(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text" validate:"required"`
	}
	if err := httpx.DecodeAndValidate(r, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	jobText := trimJD(in.Text)
	if len(jobText) < minJDChars {
		httpx.WriteError(w, httpx.ErrJDTooShort())
		return
	}

	jd, err := h.ai.AnalyzeJD(r.Context(), jobText)
	if err != nil {
		httpx.WriteError(w, mapAIError(err))
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, jd)
}

// parseCV: POST /demo/parse-cv  (multipart, field "cvFile")  -> aiclient.CVResult
func (h *Handler) parseCV(w http.ResponseWriter, r *http.Request) {
	if !h.parseMultipart(w, r) {
		return
	}
	data, filename, ok := h.extractCVFile(w, r)
	if !ok {
		return
	}

	cv, err := h.ai.ParseCV(r.Context(), filename, data)
	if err != nil {
		httpx.WriteError(w, mapAIError(err))
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, cv)
}

// match: POST /demo/match  (multipart, fields "jobText" + "cvFile") runs the
// full pipeline synchronously — AnalyzeJD, ParseCV, Match, then best-effort
// rewrites for the weakest bullets — and returns everything in one response.
// This is the one-shot endpoint for a live demo: paste a job description,
// attach a CV, get the full result back immediately, no polling required.
func (h *Handler) match(w http.ResponseWriter, r *http.Request) {
	if !h.parseMultipart(w, r) {
		return
	}
	jobText := trimJD(r.FormValue("jobText"))
	if len(jobText) < minJDChars {
		httpx.WriteError(w, httpx.ErrJDTooShort())
		return
	}
	data, filename, ok := h.extractCVFile(w, r)
	if !ok {
		return
	}

	jd, err := h.ai.AnalyzeJD(r.Context(), jobText)
	if err != nil {
		httpx.WriteError(w, mapAIError(err))
		return
	}
	cv, err := h.ai.ParseCV(r.Context(), filename, data)
	if err != nil {
		httpx.WriteError(w, mapAIError(err))
		return
	}
	m, err := h.ai.Match(r.Context(), jd, cv)
	if err != nil {
		httpx.WriteError(w, mapAIError(err))
		return
	}

	httpx.WriteSuccess(w, http.StatusOK, map[string]any{
		"jd":       jd,
		"cv":       cv,
		"match":    m,
		"rewrites": h.buildRewrites(r.Context(), m.WeakBullets, jd),
	})
}

// rewriteSuggestion mirrors analyses.RewriteSuggestion so the demo response
// shape matches what the FE/real pipeline produces.
type rewriteSuggestion struct {
	BulletID   string `json:"bulletId"`
	Original   string `json:"original"`
	Suggestion string `json:"suggestion"`
	Reasoning  string `json:"reasoning"`
}

// buildRewrites mirrors analyses.Pipeline.buildRewrites: best-effort, capped
// at 5 bullets, never fails the request if a single rewrite call errors.
func (h *Handler) buildRewrites(ctx context.Context, weak []aiclient.WeakBullet, jd aiclient.JDResult) []rewriteSuggestion {
	limit := len(weak)
	if limit > 5 {
		limit = 5
	}
	var out []rewriteSuggestion
	for _, wb := range weak[:limit] {
		r, err := h.ai.Rewrite(ctx, wb.Text, jd.Title+" | skills: "+strings.Join(jd.Skills, ", "))
		if err != nil {
			continue
		}
		out = append(out, rewriteSuggestion{
			BulletID:   wb.ID,
			Original:   wb.Text,
			Suggestion: r.Suggestion,
			Reasoning:  r.Reasoning,
		})
	}
	return out
}

// parseMultipart caps the body and parses the multipart form. Shared by every
// handler that reads a "cvFile" upload.
func (h *Handler) parseMultipart(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxCVBytes+(1<<20))
	if err := r.ParseMultipartForm(h.maxCVBytes + (1 << 20)); err != nil {
		httpx.WriteError(w, httpx.ErrCVTooLarge(h.maxCVBytes>>20))
		return false
	}
	return true
}

func (h *Handler) extractCVFile(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	file, header, err := r.FormFile("cvFile")
	if err != nil {
		httpx.WriteError(w, httpx.ErrValidation("File CV (cvFile) wajib diunggah."))
		return nil, "", false
	}
	defer file.Close()

	if header.Size > h.maxCVBytes {
		httpx.WriteError(w, httpx.ErrCVTooLarge(h.maxCVBytes>>20))
		return nil, "", false
	}
	data, err := io.ReadAll(io.LimitReader(file, h.maxCVBytes+1))
	if err != nil {
		httpx.WriteError(w, httpx.ErrInternal())
		return nil, "", false
	}
	if int64(len(data)) > h.maxCVBytes {
		httpx.WriteError(w, httpx.ErrCVTooLarge(h.maxCVBytes>>20))
		return nil, "", false
	}
	if !isPDF(data) {
		httpx.WriteError(w, httpx.ErrInvalidPDF())
		return nil, "", false
	}
	return data, header.Filename, true
}

func isPDF(data []byte) bool {
	return len(data) >= 4 && bytes.Equal(data[:4], []byte("%PDF"))
}

func trimJD(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxJDChars {
		s = s[:maxJDChars]
	}
	return s
}

func mapAIError(err error) error {
	switch {
	case errors.Is(err, aiclient.ErrCVUnreadable):
		return httpx.ErrCVUnreadable()
	case errors.Is(err, aiclient.ErrUnavailable):
		return httpx.ErrAIServiceDown()
	case errors.Is(err, aiclient.ErrBadOutput):
		return httpx.ErrAIBadOutput()
	default:
		return httpx.ErrInternal()
	}
}
