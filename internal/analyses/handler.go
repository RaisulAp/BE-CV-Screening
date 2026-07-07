package analyses

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"cvscreening/be/internal/auth"
	"cvscreening/be/internal/httpx"
	"cvscreening/be/internal/store"
)

type Handler struct {
	svc        *Service
	maxCVBytes int64
}

func NewHandler(svc *Service, maxCVBytes int64) *Handler {
	return &Handler{
		svc:        svc,
		maxCVBytes: maxCVBytes,
	}
}

// Routes mounts the analysis endpoints. Every route requires a real, logged-in
// (non-guest) identity — POST /analyses hard-gates on this; the others 404 on
// no identity via requireIdentity (a resource with no owner can't be "yours").
func (h *Handler) Routes(r chi.Router) {
	r.Post("/analyses", h.create)
	r.Get("/analyses", h.list)
	r.Get("/analyses/{id}", h.get)
	r.Post("/analyses/{id}/rescore", h.rescore)
	r.Patch("/analyses/{id}/application", h.updateApplication)
	r.Delete("/analyses/{id}", h.delete)
	r.Get("/analyses/{id}/export", h.export)
	r.Post("/rewrites", h.rewrite)
}

// requireIdentity is for by-id / mutating routes: no session → the resource is
// not theirs → 404.
func (h *Handler) requireIdentity(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	idn, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, httpx.ErrNotFound("Analisis tidak ditemukan."))
	}
	return idn, ok
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	// Reject anonymous/guest callers up front — before spending any work
	// parsing the upload. Every analysis now requires a real, logged-in
	// account (no more auto-created guest sessions).
	idn, ok := auth.IdentityFromContext(r.Context())
	if !ok || idn.IsGuest {
		httpx.WriteError(w, httpx.ErrLoginRequired())
		return
	}

	// Cap the whole request body; leave headroom over the raw PDF for the
	// multipart envelope.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxCVBytes+(1<<20))
	if err := r.ParseMultipartForm(h.maxCVBytes + (1 << 20)); err != nil {
		httpx.WriteError(w, httpx.ErrCVTooLarge(h.maxCVBytes>>20))
		return
	}

	jobText := r.FormValue("jobText")

	// Career Profile reuse path: a saved cv id instead of a fresh upload.
	if v := r.FormValue("savedCvId"); v != "" {
		savedCvID, err := strconv.ParseInt(v, 10, 64)
		if err != nil || savedCvID <= 0 {
			httpx.WriteError(w, httpx.ErrValidation("CV tersimpan tidak valid."))
			return
		}
		id, trialRemaining, err := h.svc.CreateAnalysisReuseCV(r.Context(), idn.UserID, savedCvID, jobText)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteSuccess(w, http.StatusAccepted, map[string]any{
			"analysisId":     id,
			"status":         "PENDING",
			"trialRemaining": trialRemaining,
		})
		return
	}

	file, header, err := r.FormFile("cvFile")
	if err != nil {
		httpx.WriteError(w, httpx.ErrValidation("File CV (cvFile) wajib diunggah."))
		return
	}
	defer file.Close()

	if header.Size > h.maxCVBytes {
		httpx.WriteError(w, httpx.ErrCVTooLarge(h.maxCVBytes>>20))
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, h.maxCVBytes+1))
	if err != nil {
		httpx.WriteError(w, httpx.ErrInternal())
		return
	}
	if int64(len(data)) > h.maxCVBytes {
		httpx.WriteError(w, httpx.ErrCVTooLarge(h.maxCVBytes>>20))
		return
	}
	if !isPDF(data) {
		httpx.WriteError(w, httpx.ErrInvalidPDF())
		return
	}

	id, trialRemaining, err := h.svc.CreateAnalysis(r.Context(), idn.UserID, jobText, header.Filename, data)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusAccepted, map[string]any{
		"analysisId":     id,
		"status":         "PENDING",
		"trialRemaining": trialRemaining,
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	idn, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	id, ok := parseID(chi.URLParam(r, "id"))
	if !ok {
		httpx.WriteError(w, httpx.ErrNotFound("Analisis tidak ditemukan."))
		return
	}
	view, err := h.svc.GetView(r.Context(), idn.UserID, id)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, view)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	idn, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		// No session yet → nothing to show.
		httpx.WriteSuccess(w, http.StatusOK, []store.AnalysisListItem{})
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.svc.List(r.Context(), idn.UserID, page, limit)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, items)
}

func (h *Handler) rescore(w http.ResponseWriter, r *http.Request) {
	idn, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	parentID, ok := parseID(chi.URLParam(r, "id"))
	if !ok {
		httpx.WriteError(w, httpx.ErrNotFound("Analisis tidak ditemukan."))
		return
	}
	var in struct {
		AppliedRewrites []AppliedRewrite `json:"appliedRewrites" validate:"required,min=1,dive"`
	}
	if err := httpx.DecodeAndValidate(r, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	childID, err := h.svc.Rescore(r.Context(), idn.UserID, parentID, in.AppliedRewrites)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusAccepted, map[string]any{
		"analysisId":       childID,
		"parentAnalysisId": parentID,
		"status":           "PENDING",
	})
}

// updateApplication PATCHes the tracker fields (status/deadline/notes) on a
// root analysis. Every field is optional — send only what changed. Deadline is
// an ISO date "YYYY-MM-DD" (or null to leave unchanged).
func (h *Handler) updateApplication(w http.ResponseWriter, r *http.Request) {
	idn, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	id, ok := parseID(chi.URLParam(r, "id"))
	if !ok {
		httpx.WriteError(w, httpx.ErrNotFound("Lamaran tidak ditemukan."))
		return
	}
	var in struct {
		Status   *string `json:"status"`
		Deadline *string `json:"deadline"`
		Notes    *string `json:"notes"`
	}
	if err := httpx.DecodeAndValidate(r, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}

	var deadline *time.Time
	if in.Deadline != nil && *in.Deadline != "" {
		t, err := time.Parse("2006-01-02", *in.Deadline)
		if err != nil {
			httpx.WriteError(w, httpx.ErrValidation("Format tenggat harus YYYY-MM-DD."))
			return
		}
		deadline = &t
	}

	if err := h.svc.UpdateApplication(r.Context(), idn.UserID, id, in.Status, deadline, in.Notes); err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, map[string]bool{"updated": true})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	idn, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	id, ok := parseID(chi.URLParam(r, "id"))
	if !ok {
		httpx.WriteError(w, httpx.ErrNotFound("Analisis tidak ditemukan."))
		return
	}
	if err := h.svc.Delete(r.Context(), idn.UserID, id); err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *Handler) rewrite(w http.ResponseWriter, r *http.Request) {
	idn, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	var in struct {
		AnalysisID int64  `json:"analysisId" validate:"required"`
		BulletID   string `json:"bulletId" validate:"required"`
	}
	if err := httpx.DecodeAndValidate(r, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	sug, err := h.svc.RequestRewrite(r.Context(), idn.UserID, in.AnalysisID, in.BulletID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, sug)
}

// export returns a plain-text summary (MVP). PDF export is Fase 2.
func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	idn, ok := h.requireIdentity(w, r)
	if !ok {
		return
	}
	id, ok := parseID(chi.URLParam(r, "id"))
	if !ok {
		httpx.WriteError(w, httpx.ErrNotFound("Analisis tidak ditemukan."))
		return
	}
	view, err := h.svc.GetView(r.Context(), idn.UserID, id)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}

	var b bytes.Buffer
	title, company := "-", "-"
	if view.Job.Title != nil {
		title = *view.Job.Title
	}
	if view.Job.Company != nil {
		company = *view.Job.Company
	}
	score := "-"
	if view.MatchScore != nil {
		score = strconv.Itoa(int(*view.MatchScore))
	}
	fmt.Fprintf(&b, "LAPORAN ANALISIS CV\n")
	fmt.Fprintf(&b, "===================\n")
	fmt.Fprintf(&b, "Posisi     : %s\n", title)
	fmt.Fprintf(&b, "Perusahaan : %s\n", company)
	fmt.Fprintf(&b, "File CV    : %s\n", view.Cv.FileName)
	fmt.Fprintf(&b, "Status     : %s\n", view.Status)
	fmt.Fprintf(&b, "Match Score: %s / 100\n", score)
	if view.BeforeScore != nil {
		fmt.Fprintf(&b, "Skor Awal  : %d (before/after)\n", *view.BeforeScore)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"laporan-analisis.txt\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.Bytes())
}

// isPDF checks the magic bytes rather than trusting the client mimetype (§9).
func isPDF(data []byte) bool {
	return len(data) >= 4 && bytes.Equal(data[:4], []byte("%PDF"))
}

func parseID(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
