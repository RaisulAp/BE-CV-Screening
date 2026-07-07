CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT UNIQUE,          -- NULL untuk guest (banyak NULL diizinkan)
    password_hash TEXT,                 -- NULL untuk guest
    is_guest      BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE job_descriptions (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    raw_text    TEXT NOT NULL,
    title       TEXT,
    company     TEXT,
    parsed_json JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_jd_user_created ON job_descriptions(user_id, created_at DESC);

CREATE TABLE cvs (
    id                    BIGSERIAL PRIMARY KEY,
    user_id               BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    file_name             TEXT NOT NULL,
    raw_text              TEXT,
    parsed_json           JSONB,
    structure_report_json JSONB,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_cv_user_created ON cvs(user_id, created_at DESC);

CREATE TYPE analysis_status AS ENUM ('PENDING','PROCESSING','DONE','FAILED');

CREATE TABLE analyses (
    id                       BIGSERIAL PRIMARY KEY,
    user_id                  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    job_id                   BIGINT NOT NULL REFERENCES job_descriptions(id) ON DELETE CASCADE,
    cv_id                    BIGINT NOT NULL REFERENCES cvs(id) ON DELETE CASCADE,
    parent_analysis_id       BIGINT REFERENCES analyses(id) ON DELETE CASCADE,
    status                   analysis_status NOT NULL DEFAULT 'PENDING',
    progress_step            TEXT,
    fail_reason              TEXT,
    match_score              INT,
    score_breakdown_json     JSONB,
    matched_keywords_json    JSONB,
    missing_keywords_json    JSONB,
    skill_gap_json           JSONB,
    experience_gap_json      JSONB,
    rewrite_suggestions_json JSONB,
    applied_rewrites_json    JSONB,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_analysis_user_created ON analyses(user_id, created_at DESC);
CREATE INDEX idx_analysis_parent ON analyses(parent_analysis_id);

-- FASE 2 — tabel embeddings (pgvector) sengaja belum dibuat.
