package aiclient

import (
	"context"
	"encoding/json"
)

// These shapes mirror the AI Service contract (BACKEND.md §7). The web backend
// only ever speaks this contract — it never knows about Ollama/PyMuPDF/prompts.

// JDResult ← POST /analyze/jd
type JDResult struct {
	Title      string   `json:"title"`
	Company    string   `json:"company"`
	Skills     []string `json:"skills"`
	Keywords   []string `json:"keywords"`
	Experience string   `json:"experience"`
	Education  string   `json:"education"`
}

// CVResult ← POST /parse/cv
type CVResult struct {
	RawText         string          `json:"raw_text"`
	Sections        json.RawMessage `json:"sections"`
	StructureReport json.RawMessage `json:"structure_report"`
}

// WeakBullet is a CV bullet the matcher flags as improvable (feeds Momen B).
type WeakBullet struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// MatchResult ← POST /match
type MatchResult struct {
	Score         int32           `json:"score"`
	Breakdown     json.RawMessage `json:"breakdown"`
	Matched       json.RawMessage `json:"matched"`
	Missing       json.RawMessage `json:"missing"`
	SkillGap      json.RawMessage `json:"skill_gap"`
	ExperienceGap json.RawMessage `json:"experience_gap"`
	WeakBullets   []WeakBullet    `json:"weak_bullets"`
}

// RewriteResult ← POST /rewrite
type RewriteResult struct {
	Suggestion string `json:"suggestion"`
	Reasoning  string `json:"reasoning"`
}

// Client is the single seam to the AI Service. Two implementations exist:
// HTTPClient (real) and MockClient (AI_MOCK=true) — see BACKEND.md §7/§11.
type Client interface {
	AnalyzeJD(ctx context.Context, text string) (JDResult, error)
	ParseCV(ctx context.Context, fileName string, data []byte) (CVResult, error)
	Match(ctx context.Context, jd JDResult, cv CVResult) (MatchResult, error)
	Rewrite(ctx context.Context, bullet, jdContext string) (RewriteResult, error)
}
