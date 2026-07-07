package aiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// appliedMarker is a sentinel the pipeline appends to CVResult.RawText on a
// rescore so the mock matcher can raise the score deterministically (mimicking
// a real LLM scoring an improved CV higher). Confined to the mock.
const appliedMarker = "APPLIED_REWRITES="

// MockClient returns deterministic, plausible data so the whole BE + FE can be
// built and tested without Python/Ollama (BACKEND.md §11, AI_MOCK=true).
type MockClient struct{}

func NewMockClient() *MockClient { return &MockClient{} }

func (m *MockClient) AnalyzeJD(_ context.Context, text string) (JDResult, error) {
	title, company := "Backend Engineer", "PT Contoh Teknologi"
	return JDResult{
		Title:      title,
		Company:    company,
		Skills:     []string{"Go", "REST API", "PostgreSQL", "Docker", "Kubernetes"},
		Keywords:   []string{"microservice", "CI/CD", "unit testing"},
		Experience: "Minimal 2 tahun pengalaman backend.",
		Education:  "S1 Teknik Informatika atau setara.",
	}, nil
}

func (m *MockClient) ParseCV(_ context.Context, fileName string, data []byte) (CVResult, error) {
	sections, _ := json.Marshal(map[string]any{
		"profile": "Fresh graduate Informatika, aktif organisasi.",
		"experience": []map[string]string{
			{"id": "b1", "text": "Mengurus database organisasi kampus."},
			{"id": "b2", "text": "Ikut membuat aplikasi web untuk lomba."},
			{"id": "b3", "text": "Menangani deployment aplikasi sederhana."},
		},
		"skills":    []string{"Go", "REST API", "MySQL"},
		"education": []string{"S1 Teknik Informatika"},
	})
	structure, _ := json.Marshal(map[string]any{
		"issues": []map[string]string{
			{"severity": "fatal", "type": "multi_column", "detail": "CV memakai 2 kolom — ATS sering membaca urutannya acak."},
			{"severity": "warning", "type": "photo", "detail": "Terdapat foto — sebagian ATS gagal memprosesnya."},
		},
	})
	return CVResult{
		RawText:         "Fresh graduate dengan pengalaman Go, REST API, MySQL. (file: " + fileName + ")",
		Sections:        sections,
		StructureReport: structure,
	}, nil
}

func (m *MockClient) Match(_ context.Context, _ JDResult, cv CVResult) (MatchResult, error) {
	score := int32(62)
	weak := []WeakBullet{
		{ID: "b1", Text: "Mengurus database organisasi kampus."},
		{ID: "b2", Text: "Ikut membuat aplikasi web untuk lomba."},
		{ID: "b3", Text: "Menangani deployment aplikasi sederhana."},
	}

	// On a rescore the pipeline appends APPLIED_REWRITES=n; raise the score and
	// drop the resolved weak bullets so the before/after (Momen D) is visible.
	if n := appliedCount(cv.RawText); n > 0 {
		score = int32(62 + n*9)
		if score > 95 {
			score = 95
		}
		if n >= len(weak) {
			weak = nil
		} else {
			weak = weak[n:]
		}
	}

	breakdown, _ := json.Marshal(map[string]any{
		"keyword":         map[string]any{"score": 55, "reason": "Beberapa keyword inti sudah ada, sebagian penting belum."},
		"semantic":        map[string]any{"score": 68, "reason": "Pengalaman relevan tapi kurang eksplisit."},
		"skill_coverage":  map[string]any{"score": 60, "reason": "Docker & Kubernetes belum tampak di CV."},
		"ats_readability": map[string]any{"score": 45, "reason": "Format 2 kolom menurunkan keterbacaan mesin."},
	})
	matched, _ := json.Marshal([]string{"Go", "REST API", "PostgreSQL"})
	missing, _ := json.Marshal([]string{"Docker", "Kubernetes", "CI/CD"})
	skillGap, _ := json.Marshal([]string{"Containerization (Docker)", "Orkestrasi (Kubernetes)"})
	expGap, _ := json.Marshal("Belum ada pengalaman kerja formal; tonjolkan proyek & magang.")

	return MatchResult{
		Score:         score,
		Breakdown:     breakdown,
		Matched:       matched,
		Missing:       missing,
		SkillGap:      skillGap,
		ExperienceGap: expGap,
		WeakBullets:   weak,
	}, nil
}

func (m *MockClient) Rewrite(_ context.Context, bullet, _ string) (RewriteResult, error) {
	return RewriteResult{
		Suggestion: fmt.Sprintf("Mengelola basis data organisasi kampus (PostgreSQL) untuk 500+ anggota, "+
			"meningkatkan kecepatan pencarian data 40%% — dari: %q", bullet),
		Reasoning: "Memakai formula XYZ: apa yang dikerjakan, dengan alat apa, dan dampak terukurnya.",
	}, nil
}

func appliedCount(rawText string) int {
	idx := strings.Index(rawText, appliedMarker)
	if idx < 0 {
		return 0
	}
	rest := rawText[idx+len(appliedMarker):]
	end := strings.IndexAny(rest, " \n\r\t")
	if end >= 0 {
		rest = rest[:end]
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// AppliedMarker returns the sentinel line the pipeline appends for a rescore.
func AppliedMarker(n int) string { return fmt.Sprintf("\n%s%d", appliedMarker, n) }
