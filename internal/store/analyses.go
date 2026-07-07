package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const analysisCols = `id, user_id, job_id, cv_id, parent_analysis_id, status, progress_step,
	fail_reason, match_score, score_breakdown_json, matched_keywords_json, missing_keywords_json,
	skill_gap_json, experience_gap_json, rewrite_suggestions_json, applied_rewrites_json, created_at`

func scanAnalysis(row pgx.Row) (Analysis, error) {
	var a Analysis
	err := row.Scan(
		&a.ID, &a.UserID, &a.JobID, &a.CvID, &a.ParentAnalysisID, &a.Status, &a.ProgressStep,
		&a.FailReason, &a.MatchScore, &a.ScoreBreakdownJSON, &a.MatchedKeywordsJSON, &a.MissingKeywordsJSON,
		&a.SkillGapJSON, &a.ExperienceGapJSON, &a.RewriteSuggestionsJSON, &a.AppliedRewritesJSON, &a.CreatedAt,
	)
	return a, err
}

func (s *PgStore) CreateAnalysis(ctx context.Context, userID, jobID, cvID int64, parentID *int64) (Analysis, error) {
	// Root analyses start as a 'SAVED' application (Command Center tracker);
	// child (rescore) rows leave application_status NULL — they're not a
	// separate application, just a re-score of the parent.
	appStatus := "SAVED"
	var appStatusArg any = appStatus
	if parentID != nil {
		appStatusArg = nil
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO analyses (user_id, job_id, cv_id, parent_analysis_id, status, application_status)
		 VALUES ($1, $2, $3, $4, 'PENDING', $5)
		 RETURNING `+analysisCols,
		userID, jobID, cvID, parentID, appStatusArg,
	)
	return scanAnalysis(row)
}

func (s *PgStore) GetAnalysis(ctx context.Context, id int64) (Analysis, error) {
	a, err := scanAnalysis(s.pool.QueryRow(ctx, `SELECT `+analysisCols+` FROM analyses WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Analysis{}, ErrNotFound
	}
	return a, err
}

func (s *PgStore) GetAnalysisForUser(ctx context.Context, id, userID int64) (Analysis, error) {
	a, err := scanAnalysis(s.pool.QueryRow(ctx,
		`SELECT `+analysisCols+` FROM analyses WHERE id = $1 AND user_id = $2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Analysis{}, ErrNotFound
	}
	return a, err
}

func (s *PgStore) UpdateAnalysisProgress(ctx context.Context, id int64, status string, step *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE analyses SET status = $2, progress_step = $3 WHERE id = $1`, id, status, step)
	return err
}

func (s *PgStore) UpdateAnalysisResult(ctx context.Context, id int64, m MatchResultInput) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE analyses SET
			match_score = $2, score_breakdown_json = $3, matched_keywords_json = $4,
			missing_keywords_json = $5, skill_gap_json = $6, experience_gap_json = $7
		 WHERE id = $1`,
		id, m.Score, m.Breakdown, m.Matched, m.Missing, m.SkillGap, m.ExperienceGap)
	return err
}

func (s *PgStore) UpdateAnalysisRewrites(ctx context.Context, id int64, rewrites json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE analyses SET rewrite_suggestions_json = $2 WHERE id = $1`, id, rewrites)
	return err
}

func (s *PgStore) SetAnalysisApplied(ctx context.Context, id int64, applied json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE analyses SET applied_rewrites_json = $2 WHERE id = $1`, id, applied)
	return err
}

func (s *PgStore) MarkAnalysisFailed(ctx context.Context, id int64, reason string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE analyses SET status = 'FAILED', progress_step = NULL, fail_reason = $2 WHERE id = $1`, id, reason)
	return err
}

func (s *PgStore) MarkAnalysisDone(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE analyses SET status = 'DONE', progress_step = NULL WHERE id = $1`, id)
	return err
}

func (s *PgStore) ListAnalyses(ctx context.Context, userID int64, limit, offset int) ([]AnalysisListItem, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT a.id, j.title, j.company, c.file_name, a.match_score, a.status,
			a.application_status, a.deadline, a.notes, a.created_at,
			(SELECT ch.match_score FROM analyses ch
			 WHERE ch.parent_analysis_id = a.id AND ch.match_score IS NOT NULL
			 ORDER BY ch.created_at DESC LIMIT 1) AS latest_score
		 FROM analyses a
		 JOIN job_descriptions j ON j.id = a.job_id
		 JOIN cvs c ON c.id = a.cv_id
		 WHERE a.user_id = $1 AND a.parent_analysis_id IS NULL
		 ORDER BY a.created_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AnalysisListItem
	for rows.Next() {
		var it AnalysisListItem
		if err := rows.Scan(&it.ID, &it.JobTitle, &it.Company, &it.CvFileName,
			&it.MatchScore, &it.Status, &it.ApplicationStatus, &it.Deadline, &it.Notes,
			&it.CreatedAt, &it.LatestScore); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// UpdateApplication updates the tracker fields on a root analysis (ownership-
// scoped). Nil args leave that column unchanged (COALESCE), so the FE can PATCH
// just the status, or just the notes, etc. Only root rows are applications.
func (s *PgStore) UpdateApplication(ctx context.Context, id, userID int64, status *string, deadline *time.Time, notes *string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE analyses SET
			application_status = COALESCE($3::application_status, application_status),
			deadline           = COALESCE($4::date, deadline),
			notes              = COALESCE($5::text, notes)
		 WHERE id = $1 AND user_id = $2 AND parent_analysis_id IS NULL`,
		id, userID, status, deadline, notes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) DeleteAnalysis(ctx context.Context, id, userID int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM analyses WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecoverInterrupted fails any analysis left mid-flight by a crash/restart. The
// PDF bytes only ever lived in the in-memory queue, so PENDING/PROCESSING rows
// can't be resumed — mark them FAILED honestly (BACKEND.md §3a recovery).
func (s *PgStore) RecoverInterrupted(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE analyses SET status = 'FAILED', progress_step = NULL, fail_reason = 'server_restart'
		 WHERE status IN ('PENDING','PROCESSING')`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
