package analyses

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cvscreening/be/internal/aiclient"
	"cvscreening/be/internal/httpx"
	"cvscreening/be/internal/store"
)

const (
	minJDChars = 100
	maxJDChars = 15000
)

type Service struct {
	store store.Store
	pipe  *Pipeline
	ai    aiclient.Client
	// requireEmailVerified gates analysis creation on a verified email.
	// Mirrors whether Resend is configured — see auth.Service / cmd/server/main.go wiring.
	requireEmailVerified bool
}

func NewService(st store.Store, pipe *Pipeline, ai aiclient.Client, requireEmailVerified bool) *Service {
	return &Service{
		store:                st,
		pipe:                 pipe,
		ai:                   ai,
		requireEmailVerified: requireEmailVerified,
	}
}

// View is the GET /analyses/{id} payload: the analysis plus the bits of its job
// and CV the result page needs, and the parent score for a rescore (Momen D).
type View struct {
	store.Analysis
	Job         ViewJob `json:"job"`
	Cv          ViewCv  `json:"cv"`
	BeforeScore *int32  `json:"beforeScore"`
}

type ViewJob struct {
	Title   *string `json:"title"`
	Company *string `json:"company"`
}

type ViewCv struct {
	FileName string `json:"fileName"`
	// StructureReport feeds the ATS Format Report (Momen A) on the FE. It lives
	// on the CV row; surfaced here so GET /analyses/{id} is self-contained.
	StructureReport json.RawMessage `json:"structureReport"`
	// Sections is the AI-parsed CV (profile/experience/skills/education) — feeds
	// the FE's regenerated CV preview (client only ever sees parsed text, never
	// the original PDF bytes, which are never persisted; see BACKEND.md §3b).
	Sections json.RawMessage `json:"sections"`
}

// gate runs the checks shared by every new root analysis: JD length, email
// verification, and consuming one trial credit. Returns the cleaned JD text and
// the trial credits left after the consume.
func (s *Service) gate(ctx context.Context, userID int64, jobText string) (string, int, error) {
	jobText = strings.TrimSpace(jobText)
	if len(jobText) < minJDChars {
		return "", 0, httpx.ErrJDTooShort()
	}
	if len(jobText) > maxJDChars {
		jobText = jobText[:maxJDChars]
	}

	if s.requireEmailVerified {
		user, err := s.store.GetUserByID(ctx, userID)
		if err != nil {
			return "", 0, err
		}
		if user.EmailVerifiedAt == nil {
			return "", 0, httpx.ErrEmailNotVerified()
		}
	}

	remaining, err := s.store.ConsumeTrial(ctx, userID)
	if errors.Is(err, store.ErrTrialExhausted) {
		return "", 0, httpx.ErrTrialExhausted()
	} else if err != nil {
		return "", 0, err
	}
	return jobText, remaining, nil
}

// CreateAnalysis validates the JD, consumes one trial credit, persists
// JD+CV+analysis records, then enqueues the background pipeline. Returns the
// new analysis id (status PENDING) and the trial credits left after this
// call, so the caller can update the FE badge without a second round-trip.
func (s *Service) CreateAnalysis(ctx context.Context, userID int64, jobText, cvName string, cvBytes []byte) (int64, int, error) {
	jobText, remaining, err := s.gate(ctx, userID, jobText)
	if err != nil {
		return 0, 0, err
	}

	jobRow, err := s.store.CreateJob(ctx, userID, jobText)
	if err != nil {
		return 0, 0, err
	}
	cvRow, err := s.store.CreateCV(ctx, userID, cvName)
	if err != nil {
		return 0, 0, err
	}
	a, err := s.store.CreateAnalysis(ctx, userID, jobRow.ID, cvRow.ID, nil)
	if err != nil {
		return 0, 0, err
	}

	if err := s.pipe.enqueue(job{
		analysisID: a.ID,
		jobText:    jobText,
		cvName:     cvName,
		cvBytes:    cvBytes,
	}); err != nil {
		_ = s.store.MarkAnalysisFailed(ctx, a.ID, "QUEUE_FULL")
		return 0, 0, httpx.ErrInternal()
	}
	return a.ID, remaining, nil
}

// CreateAnalysisReuseCV analyses a new JD against a saved CV (Career Profile)
// — no re-upload. Verifies ownership of savedCvID, links the new analysis to
// that existing cv row, and enqueues a reuse-cv job (skips PDF parsing).
func (s *Service) CreateAnalysisReuseCV(ctx context.Context, userID, savedCvID int64, jobText string) (int64, int, error) {
	cv, err := s.store.GetCVForUser(ctx, savedCvID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, 0, httpx.ErrNotFound("CV tersimpan tidak ditemukan.")
	} else if err != nil {
		return 0, 0, err
	}

	jobText, remaining, err := s.gate(ctx, userID, jobText)
	if err != nil {
		return 0, 0, err
	}

	jobRow, err := s.store.CreateJob(ctx, userID, jobText)
	if err != nil {
		return 0, 0, err
	}
	a, err := s.store.CreateAnalysis(ctx, userID, jobRow.ID, cv.ID, nil)
	if err != nil {
		return 0, 0, err
	}

	if err := s.pipe.enqueue(job{
		analysisID: a.ID,
		reuseCv:    true,
		jobText:    jobText,
	}); err != nil {
		_ = s.store.MarkAnalysisFailed(ctx, a.ID, "QUEUE_FULL")
		return 0, 0, httpx.ErrInternal()
	}
	return a.ID, remaining, nil
}

// Rescore creates a child analysis (Momen D) that reuses the parent's parsed JD
// and CV, then re-runs only the match step with the improved bullets. A rescore
// does not count against the guest limit (it is not a new upload).
func (s *Service) Rescore(ctx context.Context, userID, parentID int64, applied []AppliedRewrite) (int64, error) {
	parent, err := s.store.GetAnalysisForUser(ctx, parentID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, httpx.ErrNotFound("Analisis tidak ditemukan.")
	} else if err != nil {
		return 0, err
	}
	if parent.Status != store.StatusDone {
		return 0, httpx.ErrValidation("Hanya analisis yang sudah selesai yang bisa dihitung ulang.")
	}

	child, err := s.store.CreateAnalysis(ctx, userID, parent.JobID, parent.CvID, &parent.ID)
	if err != nil {
		return 0, err
	}
	if err := s.pipe.enqueue(job{analysisID: child.ID, rescore: true, applied: applied}); err != nil {
		_ = s.store.MarkAnalysisFailed(ctx, child.ID, "QUEUE_FULL")
		return 0, httpx.ErrInternal()
	}
	return child.ID, nil
}

func (s *Service) GetView(ctx context.Context, userID, id int64) (View, error) {
	a, err := s.store.GetAnalysisForUser(ctx, id, userID)
	if errors.Is(err, store.ErrNotFound) {
		return View{}, httpx.ErrNotFound("Analisis tidak ditemukan.")
	} else if err != nil {
		return View{}, err
	}

	v := View{Analysis: a}
	if jobRow, err := s.store.GetJob(ctx, a.JobID); err == nil {
		v.Job = ViewJob{Title: jobRow.Title, Company: jobRow.Company}
	}
	if cvRow, err := s.store.GetCV(ctx, a.CvID); err == nil {
		v.Cv = ViewCv{FileName: cvRow.FileName, StructureReport: cvRow.StructureReportJSON, Sections: cvRow.ParsedJSON}
	}
	if a.ParentAnalysisID != nil {
		if parent, err := s.store.GetAnalysis(ctx, *a.ParentAnalysisID); err == nil {
			v.BeforeScore = parent.MatchScore
		}
	}
	return v, nil
}

func (s *Service) List(ctx context.Context, userID int64, page, limit int) ([]store.AnalysisListItem, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}
	items, err := s.store.ListAnalyses(ctx, userID, limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []store.AnalysisListItem{}
	}
	return items, nil
}

func (s *Service) Delete(ctx context.Context, userID, id int64) error {
	err := s.store.DeleteAnalysis(ctx, id, userID)
	if errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound("Analisis tidak ditemukan.")
	}
	return err
}

// validApplicationStatuses mirrors the application_status enum in migration
// 0004 — the tracker's job-hunt pipeline stages.
var validApplicationStatuses = map[string]bool{
	"SAVED": true, "APPLIED": true, "INTERVIEW": true, "OFFER": true, "REJECTED": true,
}

// UpdateApplication changes a root analysis's tracker fields (status/deadline/
// notes). Any nil field is left unchanged. Rejects an unknown status.
func (s *Service) UpdateApplication(ctx context.Context, userID, id int64, status *string, deadline *time.Time, notes *string) error {
	if status != nil && !validApplicationStatuses[*status] {
		return httpx.ErrValidation("Status lamaran tidak valid.")
	}
	err := s.store.UpdateApplication(ctx, id, userID, status, deadline, notes)
	if errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound("Lamaran tidak ditemukan.")
	}
	return err
}

// RequestRewrite handles a single-bullet rewrite (Momen B, on-demand button).
// It resolves the bullet's original text — from the stored suggestions first,
// otherwise from the parsed CV experience section — then asks the AI.
func (s *Service) RequestRewrite(ctx context.Context, userID, analysisID int64, bulletID string) (RewriteSuggestion, error) {
	a, err := s.store.GetAnalysisForUser(ctx, analysisID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return RewriteSuggestion{}, httpx.ErrNotFound("Analisis tidak ditemukan.")
	} else if err != nil {
		return RewriteSuggestion{}, err
	}

	original, ok := findExisting(a.RewriteSuggestionsJSON, bulletID)
	if !ok {
		cvRow, err := s.store.GetCV(ctx, a.CvID)
		if err != nil {
			return RewriteSuggestion{}, httpx.ErrNotFound("CV tidak ditemukan.")
		}
		original, ok = findBulletInCV(cvRow.ParsedJSON, bulletID)
		if !ok {
			return RewriteSuggestion{}, httpx.ErrNotFound("Poin CV tidak ditemukan.")
		}
	}

	var jd aiclient.JDResult
	if jobRow, err := s.store.GetJob(ctx, a.JobID); err == nil && len(jobRow.ParsedJSON) > 0 {
		_ = json.Unmarshal(jobRow.ParsedJSON, &jd)
	}

	r, err := s.ai.Rewrite(ctx, original, jdContext(jd))
	if err != nil {
		return RewriteSuggestion{}, mapAIError(err)
	}
	return RewriteSuggestion{
		BulletID:   bulletID,
		Original:   original,
		Suggestion: r.Suggestion,
		Reasoning:  r.Reasoning,
	}, nil
}

func findExisting(raw json.RawMessage, bulletID string) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var list []RewriteSuggestion
	if json.Unmarshal(raw, &list) != nil {
		return "", false
	}
	for _, it := range list {
		if it.BulletID == bulletID {
			return it.Original, true
		}
	}
	return "", false
}

func findBulletInCV(raw json.RawMessage, bulletID string) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var sections struct {
		Experience []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"experience"`
	}
	if json.Unmarshal(raw, &sections) != nil {
		return "", false
	}
	for _, e := range sections.Experience {
		if e.ID == bulletID {
			return e.Text, true
		}
	}
	return "", false
}

func mapAIError(err error) error {
	switch {
	case errors.Is(err, aiclient.ErrUnavailable):
		return httpx.ErrAIServiceDown()
	case errors.Is(err, aiclient.ErrBadOutput):
		return httpx.ErrAIBadOutput()
	default:
		return httpx.ErrInternal()
	}
}
