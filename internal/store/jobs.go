package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *PgStore) CreateJob(ctx context.Context, userID int64, rawText string) (JobDescription, error) {
	var j JobDescription
	err := s.pool.QueryRow(ctx,
		`INSERT INTO job_descriptions (user_id, raw_text) VALUES ($1, $2)
		 RETURNING id, user_id, raw_text, title, company, parsed_json, created_at`,
		userID, rawText,
	).Scan(&j.ID, &j.UserID, &j.RawText, &j.Title, &j.Company, &j.ParsedJSON, &j.CreatedAt)
	return j, err
}

func (s *PgStore) UpdateJobParsed(ctx context.Context, id int64, title, company *string, parsed json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE job_descriptions SET title = $2, company = $3, parsed_json = $4 WHERE id = $1`,
		id, title, company, parsed,
	)
	return err
}

func (s *PgStore) GetJob(ctx context.Context, id int64) (JobDescription, error) {
	var j JobDescription
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, raw_text, title, company, parsed_json, created_at
		 FROM job_descriptions WHERE id = $1`, id,
	).Scan(&j.ID, &j.UserID, &j.RawText, &j.Title, &j.Company, &j.ParsedJSON, &j.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return JobDescription{}, ErrNotFound
	}
	return j, err
}

func (s *PgStore) ListJobs(ctx context.Context, userID int64) ([]JobDescription, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, raw_text, title, company, parsed_json, created_at
		 FROM job_descriptions WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []JobDescription
	for rows.Next() {
		var j JobDescription
		if err := rows.Scan(&j.ID, &j.UserID, &j.RawText, &j.Title, &j.Company, &j.ParsedJSON, &j.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
