package analyses

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"cvscreening/be/internal/aiclient"
	"cvscreening/be/internal/store"
)

// ErrQueueFull is returned when the in-memory backlog is saturated.
var ErrQueueFull = errors.New("analysis queue is full")

// RewriteSuggestion is one Momen B card, stored in rewrite_suggestions_json.
// BulletID is the CV-internal bullet key (e.g. "b1"), not a DB id.
type RewriteSuggestion struct {
	BulletID   string `json:"bulletId"`
	Original   string `json:"original"`
	Suggestion string `json:"suggestion"`
	Reasoning  string `json:"reasoning"`
}

// AppliedRewrite is what the FE sends on a rescore (Momen D).
type AppliedRewrite struct {
	BulletID string `json:"bulletId" validate:"required"`
	NewText  string `json:"newText" validate:"required"`
}

// job is a unit of background work. PDF bytes ride along in memory only (never
// persisted, BACKEND.md §3b); a rescore instead reuses the parent's parsed JD
// and CV from the DB.
type job struct {
	analysisID int64
	rescore    bool
	// reuseCv analyses a new JD against an already-parsed saved CV (Career
	// Profile): skips the PDF parse step, reusing cv_id's stored parsed_json.
	reuseCv bool

	// full analysis only
	jobText string
	cvName  string
	cvBytes []byte

	// rescore only
	applied []AppliedRewrite
}

// Pipeline is a single-worker FIFO queue. One inference at a time matches the
// laptop's limits and keeps Ollama from being overrun (BACKEND.md §3a/§8).
type Pipeline struct {
	jobs  chan job
	store store.Store
	ai    aiclient.Client
	log   *slog.Logger
	wg    sync.WaitGroup
}

func NewPipeline(st store.Store, ai aiclient.Client, log *slog.Logger) *Pipeline {
	return &Pipeline{
		jobs:  make(chan job, 32),
		store: st,
		ai:    ai,
		log:   log,
	}
}

// Start launches the worker goroutine. It stops when ctx is cancelled; use
// Stop to wait for an in-flight job to finish (graceful shutdown).
func (p *Pipeline) Start(ctx context.Context) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-p.jobs:
				// Use a background context so a job already in flight finishes
				// on shutdown (per-call timeouts still bound it). ctx only
				// controls whether we pick up NEW jobs.
				p.run(context.Background(), j)
			}
		}
	}()
}

// Stop blocks until the worker has drained its current job and exited.
func (p *Pipeline) Stop() { p.wg.Wait() }

func (p *Pipeline) enqueue(j job) error {
	select {
	case p.jobs <- j:
		return nil
	default:
		return ErrQueueFull
	}
}

func (p *Pipeline) run(ctx context.Context, j job) {
	if j.rescore {
		p.runRescore(ctx, j)
		return
	}
	if j.reuseCv {
		p.runReusedCV(ctx, j)
		return
	}
	p.runFull(ctx, j)
}

// runReusedCV analyses a NEW job description against a saved CV whose text is
// already parsed (Career Profile reuse). It runs AnalyzeJD like a full run,
// then loads the stored parsed CV (skipping ParseCV entirely), then matches +
// builds rewrites. This makes "apply to job #2" faster and cheaper.
func (p *Pipeline) runReusedCV(ctx context.Context, j job) {
	id := j.analysisID
	a, err := p.store.GetAnalysis(ctx, id)
	if err != nil {
		p.log.Error("pipeline: load reuse-cv analysis", "id", id, "err", err)
		return
	}

	// 1. JD → requirement (new JD)
	p.setStep(ctx, id, "analyzing_jd")
	jdText := j.jobText
	if jdText == "" {
		if jobRow, err := p.store.GetJob(ctx, a.JobID); err == nil {
			jdText = jobRow.RawText
		}
	}
	jd, err := p.ai.AnalyzeJD(ctx, jdText)
	if err != nil {
		p.fail(ctx, id, "analyze_jd", err)
		return
	}
	jdJSON, _ := json.Marshal(jd)
	if err := p.store.UpdateJobParsed(ctx, a.JobID, ptr(jd.Title), ptr(jd.Company), jdJSON); err != nil {
		p.fail(ctx, id, "save_jd", err)
		return
	}

	// 2. Load the already-parsed saved CV (skip ParseCV)
	p.setStep(ctx, id, "matching")
	cvRow, err := p.store.GetCV(ctx, a.CvID)
	if err != nil {
		p.fail(ctx, id, "load_cv", err)
		return
	}
	rawText := ""
	if cvRow.RawText != nil {
		rawText = *cvRow.RawText
	}
	cv := aiclient.CVResult{
		RawText:         rawText,
		Sections:        cvRow.ParsedJSON,
		StructureReport: cvRow.StructureReportJSON,
	}

	// 3. Match
	m, err := p.ai.Match(ctx, jd, cv)
	if err != nil {
		p.fail(ctx, id, "match", err)
		return
	}
	if err := p.store.UpdateAnalysisResult(ctx, id, toMatchInput(m)); err != nil {
		p.fail(ctx, id, "save_match", err)
		return
	}

	// 4. Best-effort rewrites (never fails the analysis)
	if rw := p.buildRewrites(ctx, m.WeakBullets, jd); len(rw) > 0 {
		rwJSON, _ := json.Marshal(rw)
		if err := p.store.UpdateAnalysisRewrites(ctx, id, rwJSON); err != nil {
			p.log.Warn("pipeline: save rewrites", "id", id, "err", err)
		}
	}

	if err := p.store.MarkAnalysisDone(ctx, id); err != nil {
		p.log.Error("pipeline: mark reuse-cv done", "id", id, "err", err)
	}
}

func (p *Pipeline) runFull(ctx context.Context, j job) {
	id := j.analysisID
	a, err := p.store.GetAnalysis(ctx, id)
	if err != nil {
		p.log.Error("pipeline: load analysis", "id", id, "err", err)
		return
	}

	// 1. JD → requirement
	p.setStep(ctx, id, "analyzing_jd")
	jdText := j.jobText
	if jdText == "" {
		if jobRow, err := p.store.GetJob(ctx, a.JobID); err == nil {
			jdText = jobRow.RawText
		}
	}
	jd, err := p.ai.AnalyzeJD(ctx, jdText)
	if err != nil {
		p.fail(ctx, id, "analyze_jd", err)
		return
	}
	jdJSON, _ := json.Marshal(jd)
	if err := p.store.UpdateJobParsed(ctx, a.JobID, ptr(jd.Title), ptr(jd.Company), jdJSON); err != nil {
		p.fail(ctx, id, "save_jd", err)
		return
	}

	// 2. CV PDF → text + structure
	p.setStep(ctx, id, "parsing_cv")
	cv, err := p.ai.ParseCV(ctx, j.cvName, j.cvBytes)
	if err != nil {
		p.fail(ctx, id, "parse_cv", err)
		return
	}
	if err := p.store.UpdateCVParsed(ctx, a.CvID, cv.RawText, cv.Sections, cv.StructureReport); err != nil {
		p.fail(ctx, id, "save_cv", err)
		return
	}

	// 3. Match
	p.setStep(ctx, id, "matching")
	m, err := p.ai.Match(ctx, jd, cv)
	if err != nil {
		p.fail(ctx, id, "match", err)
		return
	}
	if err := p.store.UpdateAnalysisResult(ctx, id, toMatchInput(m)); err != nil {
		p.fail(ctx, id, "save_match", err)
		return
	}

	// 4. Best-effort rewrites for the weakest bullets (never fails the analysis)
	if rw := p.buildRewrites(ctx, m.WeakBullets, jd); len(rw) > 0 {
		rwJSON, _ := json.Marshal(rw)
		if err := p.store.UpdateAnalysisRewrites(ctx, id, rwJSON); err != nil {
			p.log.Warn("pipeline: save rewrites", "id", id, "err", err)
		}
	}

	// 5. Done
	if err := p.store.MarkAnalysisDone(ctx, id); err != nil {
		p.log.Error("pipeline: mark done", "id", id, "err", err)
	}
}

func (p *Pipeline) runRescore(ctx context.Context, j job) {
	id := j.analysisID
	a, err := p.store.GetAnalysis(ctx, id)
	if err != nil {
		p.log.Error("pipeline: load rescore analysis", "id", id, "err", err)
		return
	}

	p.setStep(ctx, id, "matching")

	// Reuse the parent's parsed JD and CV (same job_id/cv_id on the child).
	jobRow, err := p.store.GetJob(ctx, a.JobID)
	if err != nil {
		p.fail(ctx, id, "load_job", err)
		return
	}
	var jd aiclient.JDResult
	if len(jobRow.ParsedJSON) > 0 {
		_ = json.Unmarshal(jobRow.ParsedJSON, &jd)
	}

	cvRow, err := p.store.GetCV(ctx, a.CvID)
	if err != nil {
		p.fail(ctx, id, "load_cv", err)
		return
	}
	rawText := ""
	if cvRow.RawText != nil {
		rawText = *cvRow.RawText
	}
	// Splice the actually-improved bullet text into the CV before re-matching,
	// so a real AI matcher scores the CV the user really has (not a bump
	// derived from a count). The AppliedMarker suffix is kept only so
	// MockClient's deterministic demo bump still works under AI_MOCK=true.
	rawText, sections := applyRewrites(rawText, cvRow.ParsedJSON, j.applied)
	cv := aiclient.CVResult{
		RawText:         rawText + aiclient.AppliedMarker(len(j.applied)),
		Sections:        sections,
		StructureReport: cvRow.StructureReportJSON,
	}

	m, err := p.ai.Match(ctx, jd, cv)
	if err != nil {
		p.fail(ctx, id, "rescore_match", err)
		return
	}
	if err := p.store.UpdateAnalysisResult(ctx, id, toMatchInput(m)); err != nil {
		p.fail(ctx, id, "save_rescore", err)
		return
	}
	appliedJSON, _ := json.Marshal(j.applied)
	_ = p.store.SetAnalysisApplied(ctx, id, appliedJSON)

	if err := p.store.MarkAnalysisDone(ctx, id); err != nil {
		p.log.Error("pipeline: mark rescore done", "id", id, "err", err)
	}
}

func (p *Pipeline) buildRewrites(ctx context.Context, weak []aiclient.WeakBullet, jd aiclient.JDResult) []RewriteSuggestion {
	limit := len(weak)
	if limit > 5 {
		limit = 5
	}
	var out []RewriteSuggestion
	for _, wb := range weak[:limit] {
		r, err := p.ai.Rewrite(ctx, wb.Text, jdContext(jd))
		if err != nil {
			p.log.Warn("pipeline: rewrite bullet", "bullet", wb.ID, "err", err)
			continue
		}
		out = append(out, RewriteSuggestion{
			BulletID:   wb.ID,
			Original:   wb.Text,
			Suggestion: r.Suggestion,
			Reasoning:  r.Reasoning,
		})
	}
	return out
}

func (p *Pipeline) setStep(ctx context.Context, id int64, step string) {
	if err := p.store.UpdateAnalysisProgress(ctx, id, store.StatusProcessing, &step); err != nil {
		p.log.Warn("pipeline: set step", "id", id, "step", step, "err", err)
	}
}

func (p *Pipeline) fail(ctx context.Context, id int64, where string, err error) {
	reason := failReason(err)
	p.log.Warn("pipeline: step failed", "id", id, "where", where, "reason", reason, "err", err)
	if e := p.store.MarkAnalysisFailed(ctx, id, reason); e != nil {
		p.log.Error("pipeline: mark failed", "id", id, "err", e)
	}
}

func failReason(err error) string {
	switch {
	case errors.Is(err, aiclient.ErrUnavailable):
		return "AI_SERVICE_DOWN"
	case errors.Is(err, aiclient.ErrCVUnreadable):
		return "CV_UNREADABLE"
	case errors.Is(err, aiclient.ErrBadOutput):
		return "AI_BAD_OUTPUT"
	default:
		return "INTERNAL"
	}
}

func toMatchInput(m aiclient.MatchResult) store.MatchResultInput {
	return store.MatchResultInput{
		Score:         m.Score,
		Breakdown:     m.Breakdown,
		Matched:       m.Matched,
		Missing:       m.Missing,
		SkillGap:      m.SkillGap,
		ExperienceGap: m.ExperienceGap,
	}
}

func jdContext(jd aiclient.JDResult) string {
	return jd.Title + " | skills: " + strings.Join(jd.Skills, ", ")
}

// bulletEntry mirrors one item of cv.sections.experience[] (see
// aiclient.CVResult / BE/internal/analyses/service.go:findBulletInCV).
type bulletEntry struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// applyRewrites splices each AppliedRewrite's new text into the CV's
// experience bullets (matched by id) and, best-effort, into rawText wherever
// the original bullet text appears verbatim. Unknown top-level fields in
// sections (profile, skills, education, ...) are preserved untouched. If
// sections don't parse as expected, the original values are returned as-is.
func applyRewrites(rawText string, sections json.RawMessage, applied []AppliedRewrite) (string, json.RawMessage) {
	if len(applied) == 0 || len(sections) == 0 {
		return rawText, sections
	}

	var doc map[string]json.RawMessage
	if json.Unmarshal(sections, &doc) != nil {
		return rawText, sections
	}
	expRaw, ok := doc["experience"]
	if !ok {
		return rawText, sections
	}
	var bullets []bulletEntry
	if json.Unmarshal(expRaw, &bullets) != nil {
		return rawText, sections
	}

	byID := make(map[string]string, len(applied))
	for _, ar := range applied {
		byID[ar.BulletID] = ar.NewText
	}

	changed := false
	for i, b := range bullets {
		newText, ok := byID[b.ID]
		if !ok {
			continue
		}
		if b.Text != "" {
			rawText = strings.Replace(rawText, b.Text, newText, 1)
		}
		bullets[i].Text = newText
		changed = true
	}
	if !changed {
		return rawText, sections
	}

	newExp, err := json.Marshal(bullets)
	if err != nil {
		return rawText, sections
	}
	doc["experience"] = newExp
	out, err := json.Marshal(doc)
	if err != nil {
		return rawText, sections
	}
	return rawText, out
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
