package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

const cvCols = `id, user_id, file_name, label, archived, raw_text, parsed_json, structure_report_json, created_at`

func scanCV(row pgx.Row) (Cv, error) {
	var c Cv
	err := row.Scan(&c.ID, &c.UserID, &c.FileName, &c.Label, &c.Archived,
		&c.RawText, &c.ParsedJSON, &c.StructureReportJSON, &c.CreatedAt)
	return c, err
}

func (s *PgStore) CreateCV(ctx context.Context, userID int64, fileName string) (Cv, error) {
	return scanCV(s.pool.QueryRow(ctx,
		`INSERT INTO cvs (user_id, file_name) VALUES ($1, $2) RETURNING `+cvCols,
		userID, fileName))
}

func (s *PgStore) UpdateCVParsed(ctx context.Context, id int64, rawText string, parsed, structure json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cvs SET raw_text = $2, parsed_json = $3, structure_report_json = $4 WHERE id = $1`,
		id, rawText, parsed, structure,
	)
	return err
}

func (s *PgStore) GetCV(ctx context.Context, id int64) (Cv, error) {
	c, err := scanCV(s.pool.QueryRow(ctx, `SELECT `+cvCols+` FROM cvs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Cv{}, ErrNotFound
	}
	return c, err
}

// GetCVForUser resolves a CV scoped to its owner — used before reusing a saved
// CV for a new analysis, so you can't analyse with someone else's CV id.
func (s *PgStore) GetCVForUser(ctx context.Context, id, userID int64) (Cv, error) {
	c, err := scanCV(s.pool.QueryRow(ctx,
		`SELECT `+cvCols+` FROM cvs WHERE id = $1 AND user_id = $2 AND archived = false`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Cv{}, ErrNotFound
	}
	return c, err
}

// ListCVLibrary returns the user's saved (non-archived) CVs as library cards
// with light analytics: how many root analyses used each CV, and its most
// recent score.
func (s *PgStore) ListCVLibrary(ctx context.Context, userID int64) ([]CvLibraryItem, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.id, c.file_name, c.label,
			(SELECT count(*) FROM analyses a WHERE a.cv_id = c.id AND a.parent_analysis_id IS NULL),
			(SELECT a.match_score FROM analyses a
			 WHERE a.cv_id = c.id AND a.match_score IS NOT NULL
			 ORDER BY a.created_at DESC LIMIT 1),
			c.created_at
		 FROM cvs c
		 WHERE c.user_id = $1 AND c.archived = false
		 ORDER BY c.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CvLibraryItem
	for rows.Next() {
		var it CvLibraryItem
		if err := rows.Scan(&it.ID, &it.FileName, &it.Label, &it.AnalysisCount, &it.LastScore, &it.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *PgStore) RenameCV(ctx context.Context, id, userID int64, label string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE cvs SET label = $3 WHERE id = $1 AND user_id = $2 AND archived = false`,
		id, userID, label)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ArchiveCV soft-hides a CV from the library (its analyses/history stay intact).
func (s *PgStore) ArchiveCV(ctx context.Context, id, userID int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE cvs SET archived = true WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
