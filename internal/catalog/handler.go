// Package catalog serves the secondary read-only lists (saved JDs and CVs) used
// for "apply to a similar role again" (BACKEND.md §6). Metadata only.
package catalog

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"cvscreening/be/internal/auth"
	"cvscreening/be/internal/httpx"
	"cvscreening/be/internal/store"
)

type Handler struct {
	store store.Store
}

func NewHandler(st store.Store) *Handler { return &Handler{store: st} }

func (h *Handler) Routes(r chi.Router) {
	r.Get("/jobs", h.listJobs)
	r.Get("/cvs", h.listCVs)          // CV Library cards (with analytics)
	r.Patch("/cvs/{id}", h.renameCV)  // rename saved CV
	r.Delete("/cvs/{id}", h.archiveCV) // soft-hide from library
}

func (h *Handler) listJobs(w http.ResponseWriter, r *http.Request) {
	idn, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteSuccess(w, http.StatusOK, []store.JobDescription{})
		return
	}
	jobs, err := h.store.ListJobs(r.Context(), idn.UserID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if jobs == nil {
		jobs = []store.JobDescription{}
	}
	httpx.WriteSuccess(w, http.StatusOK, jobs)
}

func (h *Handler) listCVs(w http.ResponseWriter, r *http.Request) {
	idn, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteSuccess(w, http.StatusOK, []store.CvLibraryItem{})
		return
	}
	cvs, err := h.store.ListCVLibrary(r.Context(), idn.UserID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if cvs == nil {
		cvs = []store.CvLibraryItem{}
	}
	httpx.WriteSuccess(w, http.StatusOK, cvs)
}

func (h *Handler) renameCV(w http.ResponseWriter, r *http.Request) {
	idn, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, httpx.ErrNotFound("CV tidak ditemukan."))
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, httpx.ErrNotFound("CV tidak ditemukan."))
		return
	}
	var in struct {
		Label string `json:"label" validate:"required,max=80"`
	}
	if err := httpx.DecodeAndValidate(r, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if err := h.store.RenameCV(r.Context(), id, idn.UserID, in.Label); err != nil {
		if err == store.ErrNotFound {
			httpx.WriteError(w, httpx.ErrNotFound("CV tidak ditemukan."))
			return
		}
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, map[string]bool{"updated": true})
}

func (h *Handler) archiveCV(w http.ResponseWriter, r *http.Request) {
	idn, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, httpx.ErrNotFound("CV tidak ditemukan."))
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, httpx.ErrNotFound("CV tidak ditemukan."))
		return
	}
	if err := h.store.ArchiveCV(r.Context(), id, idn.UserID); err != nil {
		if err == store.ErrNotFound {
			httpx.WriteError(w, httpx.ErrNotFound("CV tidak ditemukan."))
			return
		}
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteSuccess(w, http.StatusOK, map[string]bool{"archived": true})
}
