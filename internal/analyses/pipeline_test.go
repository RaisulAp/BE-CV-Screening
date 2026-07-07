package analyses

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyRewrites(t *testing.T) {
	sections := json.RawMessage(`{
		"profile": "Fresh graduate Informatika.",
		"experience": [
			{"id": "b1", "text": "Mengurus database organisasi kampus."},
			{"id": "b2", "text": "Ikut membuat aplikasi web untuk lomba."}
		],
		"skills": ["Go", "REST API"],
		"education": ["S1 Teknik Informatika"]
	}`)
	rawText := "Fresh graduate. Mengurus database organisasi kampus. Ikut membuat aplikasi web untuk lomba."

	applied := []AppliedRewrite{
		{BulletID: "b1", NewText: "Mengelola basis data organisasi kampus (PostgreSQL) untuk 500+ anggota."},
	}

	newRawText, newSections := applyRewrites(rawText, sections, applied)

	if strings.Contains(newRawText, "Mengurus database organisasi kampus.") {
		t.Errorf("expected old bullet text to be replaced in rawText, got: %s", newRawText)
	}
	if !strings.Contains(newRawText, "Mengelola basis data organisasi kampus") {
		t.Errorf("expected new bullet text in rawText, got: %s", newRawText)
	}
	if !strings.Contains(newRawText, "Ikut membuat aplikasi web untuk lomba.") {
		t.Errorf("expected untouched bullet b2 to remain in rawText, got: %s", newRawText)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(newSections, &doc); err != nil {
		t.Fatalf("sections did not round-trip as valid JSON: %v", err)
	}
	var bullets []bulletEntry
	if err := json.Unmarshal(doc["experience"], &bullets); err != nil {
		t.Fatalf("experience did not round-trip: %v", err)
	}
	if len(bullets) != 2 {
		t.Fatalf("expected 2 bullets, got %d", len(bullets))
	}
	if bullets[0].Text != applied[0].NewText {
		t.Errorf("b1 text not updated: got %q", bullets[0].Text)
	}
	if bullets[1].Text != "Ikut membuat aplikasi web untuk lomba." {
		t.Errorf("b2 text should be untouched, got %q", bullets[1].Text)
	}

	var skills []string
	if err := json.Unmarshal(doc["skills"], &skills); err != nil || len(skills) != 2 {
		t.Errorf("expected skills field preserved untouched, got %v (err=%v)", skills, err)
	}
}

func TestApplyRewritesNoop(t *testing.T) {
	sections := json.RawMessage(`{"experience": [{"id": "b1", "text": "asli"}]}`)

	// No applied rewrites: sections/rawText pass through unchanged.
	rawText, out := applyRewrites("teks asli", sections, nil)
	if rawText != "teks asli" || string(out) != string(sections) {
		t.Errorf("expected passthrough with no applied rewrites, got rawText=%q sections=%s", rawText, out)
	}

	// Empty sections: passthrough without panicking.
	rawText, out = applyRewrites("teks", nil, []AppliedRewrite{{BulletID: "b1", NewText: "baru"}})
	if rawText != "teks" || out != nil {
		t.Errorf("expected passthrough with empty sections, got rawText=%q sections=%v", rawText, out)
	}

	// Bullet id not present in sections: no change, no panic.
	rawText, out = applyRewrites("teks asli", sections, []AppliedRewrite{{BulletID: "nonexistent", NewText: "baru"}})
	if rawText != "teks asli" || string(out) != string(sections) {
		t.Errorf("expected passthrough when bullet id not found, got rawText=%q sections=%s", rawText, out)
	}
}
