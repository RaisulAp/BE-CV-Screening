package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	migrations "cvscreening/be/db"
	"cvscreening/be/internal/aiclient"
	"cvscreening/be/internal/analyses"
	"cvscreening/be/internal/auth"
	"cvscreening/be/internal/captcha"
	"cvscreening/be/internal/catalog"
	"cvscreening/be/internal/config"
	"cvscreening/be/internal/email"
	"cvscreening/be/internal/server"
	"cvscreening/be/internal/store"
)

const testDBURL = "postgres://postgres:123456789@localhost:5432/cvscreening_test?sslmode=disable"

const longJD = "Kami mencari Backend Engineer berpengalaman dengan Go, REST API, PostgreSQL, Docker, dan Kubernetes. " +
	"Tanggung jawab meliputi membangun microservice, menulis unit test, dan mengelola CI/CD pipeline."

var fakePDF = []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF")

func dbURL() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return testDBURL
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dbURL())
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	_, _ = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
	pool.Close()

	st, err := store.New(ctx, dbURL())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := st.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ai := aiclient.NewMockClient()

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	pipe := analyses.NewPipeline(st, ai, log)
	pipe.Start(workerCtx)

	cfg := config.Config{
		JWTSecret:    "test-secret",
		JWTExpiresIn: time.Hour,
		AIMock:       true,
		FrontendURL:  "http://localhost:3000",
		MaxCVSizeMB:  5,
	}
	emailClient := email.NewClient("", "")     // disabled in tests: no RESEND_API_KEY
	captchaVerifier := captcha.NewVerifier("") // disabled in tests: no TURNSTILE_SECRET_KEY
	authSvc := auth.NewService(st, cfg.JWTSecret, cfg.JWTExpiresIn, emailClient, cfg.FrontendURL)
	analysisSvc := analyses.NewService(st, pipe, ai, emailClient.Enabled())
	router := server.NewRouter(server.Deps{
		Cfg:         cfg,
		Log:         log,
		AuthHandler: auth.NewHandler(authSvc, captchaVerifier, cfg.CookieMaxAge(), cfg.CookieSecure),
		AnalysisH:   analyses.NewHandler(analysisSvc, cfg.MaxCVSizeBytes()),
		CatalogH:    catalog.NewHandler(st),
	})

	ts := httptest.NewServer(router)
	t.Cleanup(func() {
		ts.Close()
		cancelWorker()
		pipe.Stop()
		st.Close()
	})
	return ts
}

// --- helpers ---

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// newClient returns an HTTP client with its own cookie jar (its own session).
func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{Jar: jar}
}

func doJSON(t *testing.T, c *http.Client, method, url string, body any) (*http.Response, envelope) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	var env envelope
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	_ = json.Unmarshal(raw, &env)
	return resp, env
}

func register(t *testing.T, c *http.Client, ts *httptest.Server, email string) {
	t.Helper()
	resp, env := doJSON(t, c, http.MethodPost, ts.URL+"/auth/register",
		map[string]string{"email": email, "password": "password123"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, err=%s", resp.StatusCode, env.Error.Code)
	}
	// Token must NOT be in the body — it lives in the cookie.
	if strings.Contains(string(env.Data), "accessToken") {
		t.Fatal("register leaked token in body; expected cookie-only")
	}
	if !hasAuthCookie(c, ts) {
		t.Fatal("register did not set auth cookie")
	}
}

func hasAuthCookie(c *http.Client, ts *httptest.Server) bool {
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	for _, ck := range c.Jar.Cookies(req.URL) {
		if ck.Name == auth.CookieName && ck.Value != "" {
			return true
		}
	}
	return false
}

func uploadAnalysis(t *testing.T, c *http.Client, ts *httptest.Server, jobText string, pdf []byte, fileName string) (*http.Response, envelope) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("jobText", jobText)
	fw, _ := mw.CreateFormFile("cvFile", fileName)
	_, _ = fw.Write(pdf)
	_ = mw.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/analyses", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	var env envelope
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	_ = json.Unmarshal(raw, &env)
	return resp, env
}

func createID(t *testing.T, c *http.Client, ts *httptest.Server) int64 {
	t.Helper()
	resp, env := uploadAnalysis(t, c, ts, longJD, fakePDF, "cv.pdf")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create status = %d, err=%s", resp.StatusCode, env.Error.Code)
	}
	var created struct {
		AnalysisID int64 `json:"analysisId"`
	}
	_ = json.Unmarshal(env.Data, &created)
	if created.AnalysisID == 0 {
		t.Fatal("create returned no analysisId")
	}
	return created.AnalysisID
}

// analyzeWithSavedCV posts a new JD against an already-saved cv id (no upload).
func analyzeWithSavedCV(t *testing.T, c *http.Client, ts *httptest.Server, jobText string, cvID int64) (*http.Response, envelope) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("jobText", jobText)
	_ = mw.WriteField("savedCvId", strconv.FormatInt(cvID, 10))
	_ = mw.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/analyses", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("analyze saved cv: %v", err)
	}
	var env envelope
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	_ = json.Unmarshal(raw, &env)
	return resp, env
}

func pollUntilDone(t *testing.T, c *http.Client, ts *httptest.Server, id int64) store.Analysis {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, env := doJSON(t, c, http.MethodGet, ts.URL+"/analyses/"+itoa(id), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("poll status = %d, err=%s", resp.StatusCode, env.Error.Code)
		}
		var a store.Analysis
		_ = json.Unmarshal(env.Data, &a)
		if a.Status == store.StatusDone || a.Status == store.StatusFailed {
			return a
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("analysis did not finish in time")
	return store.Analysis{}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// --- tests ---

// TestAnonymousUploadRejected guards the auth pivot: there is no more guest
// tier — every analysis requires a real, logged-in account. An anonymous
// visitor must be rejected outright, and (unlike the old guest flow) no
// session cookie should be minted for them in the process.
func TestAnonymousUploadRejected(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t) // brand-new visitor, no cookie, never registers

	resp, env := uploadAnalysis(t, c, ts, longJD, fakePDF, "cv.pdf")
	if resp.StatusCode != http.StatusUnauthorized || env.Error.Code != "LOGIN_REQUIRED" {
		t.Fatalf("anonymous upload: expected 401 LOGIN_REQUIRED, got %d %s", resp.StatusCode, env.Error.Code)
	}
	if hasAuthCookie(c, ts) {
		t.Fatal("anonymous upload must not mint a session cookie")
	}

	// Registering afterwards works normally and starts with a full trial.
	register(t, c, ts, "fresh@example.com")
	resp2, env2 := uploadAnalysis(t, c, ts, longJD, fakePDF, "cv.pdf")
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("post-register analysis status = %d, err=%s", resp2.StatusCode, env2.Error.Code)
	}
}

func TestHappyPath_AnalyzeRescoreHistory(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t)
	register(t, c, ts, "happy@example.com")

	id := createID(t, c, ts)
	a := pollUntilDone(t, c, ts, id)
	if a.Status != store.StatusDone {
		t.Fatalf("expected DONE, got %s (reason=%v)", a.Status, a.FailReason)
	}
	if a.ID != id {
		t.Fatalf("id mismatch: %d vs %d", a.ID, id)
	}
	if a.MatchScore == nil || *a.MatchScore != 62 {
		t.Fatalf("expected score 62, got %v", a.MatchScore)
	}
	if len(a.RewriteSuggestionsJSON) == 0 {
		t.Fatal("expected rewrite suggestions to be populated")
	}

	// Rescore → score must rise (Momen D).
	respR, envR := doJSON(t, c, http.MethodPost, ts.URL+"/analyses/"+itoa(id)+"/rescore",
		map[string]any{"appliedRewrites": []map[string]string{
			{"bulletId": "b1", "newText": "Mengelola PostgreSQL untuk 500+ anggota."},
			{"bulletId": "b2", "newText": "Membangun aplikasi web pemenang lomba."},
		}})
	if respR.StatusCode != http.StatusAccepted {
		t.Fatalf("rescore status = %d, err=%s", respR.StatusCode, envR.Error.Code)
	}
	var child struct {
		AnalysisID       int64 `json:"analysisId"`
		ParentAnalysisID int64 `json:"parentAnalysisId"`
	}
	_ = json.Unmarshal(envR.Data, &child)
	if child.ParentAnalysisID != id {
		t.Fatalf("parent mismatch: %d != %d", child.ParentAnalysisID, id)
	}

	rescored := pollUntilDone(t, c, ts, child.AnalysisID)
	if rescored.MatchScore == nil || *rescored.MatchScore <= *a.MatchScore {
		t.Fatalf("rescore should improve: before=%d after=%v", *a.MatchScore, rescored.MatchScore)
	}

	// beforeScore exposed on the child view.
	_, envView := doJSON(t, c, http.MethodGet, ts.URL+"/analyses/"+itoa(child.AnalysisID), nil)
	var view struct {
		BeforeScore *int32 `json:"beforeScore"`
	}
	_ = json.Unmarshal(envView.Data, &view)
	if view.BeforeScore == nil || *view.BeforeScore != 62 {
		t.Fatalf("expected beforeScore 62, got %v", view.BeforeScore)
	}

	// History root has latestScore = child score.
	_, envH := doJSON(t, c, http.MethodGet, ts.URL+"/analyses?page=1&limit=10", nil)
	var items []store.AnalysisListItem
	_ = json.Unmarshal(envH.Data, &items)
	if len(items) != 1 {
		t.Fatalf("expected 1 history root, got %d", len(items))
	}
	if items[0].LatestScore == nil || *items[0].LatestScore != *rescored.MatchScore {
		t.Fatalf("history latestScore mismatch: %v vs %d", items[0].LatestScore, *rescored.MatchScore)
	}

	// Single-bullet rewrite (Momen B on-demand).
	respRw, envRw := doJSON(t, c, http.MethodPost, ts.URL+"/rewrites",
		map[string]any{"analysisId": id, "bulletId": "b3"})
	if respRw.StatusCode != http.StatusOK {
		t.Fatalf("rewrite status = %d, err=%s", respRw.StatusCode, envRw.Error.Code)
	}

	// Delete removes it from history.
	respD, _ := doJSON(t, c, http.MethodDelete, ts.URL+"/analyses/"+itoa(id), nil)
	if respD.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", respD.StatusCode)
	}
	_, envH2 := doJSON(t, c, http.MethodGet, ts.URL+"/analyses?page=1&limit=10", nil)
	var items2 []store.AnalysisListItem
	_ = json.Unmarshal(envH2.Data, &items2)
	if len(items2) != 0 {
		t.Fatalf("expected empty history after delete, got %d", len(items2))
	}
}

func TestReject_NonPDF(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t)
	register(t, c, ts, "nonpdf@example.com") // validation only runs for a logged-in identity
	resp, env := uploadAnalysis(t, c, ts, longJD, []byte("this is not a pdf"), "cv.pdf")
	if resp.StatusCode != http.StatusBadRequest || env.Error.Code != "INVALID_PDF" {
		t.Fatalf("expected 400 INVALID_PDF, got %d %s", resp.StatusCode, env.Error.Code)
	}
}

func TestReject_ShortJD(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t)
	register(t, c, ts, "shortjd@example.com") // validation only runs for a logged-in identity
	resp, env := uploadAnalysis(t, c, ts, "terlalu pendek", fakePDF, "cv.pdf")
	if resp.StatusCode != http.StatusBadRequest || env.Error.Code != "JD_TOO_SHORT" {
		t.Fatalf("expected 400 JD_TOO_SHORT, got %d %s", resp.StatusCode, env.Error.Code)
	}
}

func TestNoSession(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t)

	// /auth/me without a cookie → 401.
	resp, _ := doJSON(t, c, http.MethodGet, ts.URL+"/auth/me", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me without session: expected 401, got %d", resp.StatusCode)
	}

	// History without a session → empty list, not an error.
	resp2, env2 := doJSON(t, c, http.MethodGet, ts.URL+"/analyses?page=1&limit=10", nil)
	if resp2.StatusCode != http.StatusOK || strings.TrimSpace(string(env2.Data)) != "[]" {
		t.Fatalf("history without session: expected 200 [], got %d %s", resp2.StatusCode, string(env2.Data))
	}
}

func TestOwnership_404ForOtherUser(t *testing.T) {
	ts := newTestServer(t)
	ca := newClient(t)
	cb := newClient(t)
	register(t, ca, ts, "owner-a@example.com")
	register(t, cb, ts, "owner-b@example.com")

	id := createID(t, ca, ts)

	resp, env := doJSON(t, cb, http.MethodGet, ts.URL+"/analyses/"+itoa(id), nil)
	if resp.StatusCode != http.StatusNotFound || env.Error.Code != "NOT_FOUND" {
		t.Fatalf("expected 404 NOT_FOUND for other user, got %d %s", resp.StatusCode, env.Error.Code)
	}
}

func TestDuplicateEmail(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t)
	register(t, c, ts, "dupe@example.com")

	c2 := newClient(t)
	resp, env := doJSON(t, c2, http.MethodPost, ts.URL+"/auth/register",
		map[string]string{"email": "dupe@example.com", "password": "password123"})
	if resp.StatusCode != http.StatusConflict || env.Error.Code != "EMAIL_TAKEN" {
		t.Fatalf("expected 409 EMAIL_TAKEN, got %d %s", resp.StatusCode, env.Error.Code)
	}
}

func TestLoginAndLogout(t *testing.T) {
	ts := newTestServer(t)
	email := "login@example.com"
	reg := newClient(t)
	register(t, reg, ts, email)

	// Fresh client logs in → cookie set, no token in body.
	c := newClient(t)
	resp, env := doJSON(t, c, http.MethodPost, ts.URL+"/auth/login",
		map[string]string{"email": email, "password": "password123"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, err=%s", resp.StatusCode, env.Error.Code)
	}
	if strings.Contains(string(env.Data), "accessToken") {
		t.Fatal("login leaked token in body; expected cookie-only")
	}
	if !hasAuthCookie(c, ts) {
		t.Fatal("login did not set auth cookie")
	}

	// /auth/me works with the cookie.
	resp2, env2 := doJSON(t, c, http.MethodGet, ts.URL+"/auth/me", nil)
	if resp2.StatusCode != http.StatusOK || !strings.Contains(string(env2.Data), email) {
		t.Fatalf("me failed: %d %s", resp2.StatusCode, string(env2.Data))
	}

	// Logout clears the cookie → me now 401.
	respL, _ := doJSON(t, c, http.MethodPost, ts.URL+"/auth/logout", nil)
	if respL.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d", respL.StatusCode)
	}
	resp3, _ := doJSON(t, c, http.MethodGet, ts.URL+"/auth/me", nil)
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me after logout: expected 401, got %d", resp3.StatusCode)
	}

	// Wrong password.
	c2 := newClient(t)
	resp4, env4 := doJSON(t, c2, http.MethodPost, ts.URL+"/auth/login",
		map[string]string{"email": email, "password": "wrongpassword"})
	if resp4.StatusCode != http.StatusUnauthorized || env4.Error.Code != "INVALID_CREDENTIALS" {
		t.Fatalf("expected 401 INVALID_CREDENTIALS, got %d %s", resp4.StatusCode, env4.Error.Code)
	}
}

// TestTrialQuota guards the login-gated trial pivot: every fresh account gets
// exactly 3 lifetime trial credits (internal/store/billing.go), and the 4th
// root analysis must be rejected with 402 TRIAL_EXHAUSTED — there is no more
// monthly rolling quota or guest tier to fall back to.
func TestTrialQuota(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t)
	register(t, c, ts, "trial@example.com")

	for i := 0; i < 3; i++ {
		resp, env := uploadAnalysis(t, c, ts, longJD, fakePDF, "cv.pdf")
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("trial analysis #%d status = %d, err=%s", i+1, resp.StatusCode, env.Error.Code)
		}
	}

	resp, env := uploadAnalysis(t, c, ts, longJD, fakePDF, "cv.pdf")
	if resp.StatusCode != http.StatusPaymentRequired || env.Error.Code != "TRIAL_EXHAUSTED" {
		t.Fatalf("4th analysis: expected 402 TRIAL_EXHAUSTED, got %d %s", resp.StatusCode, env.Error.Code)
	}
}

// TestApplicationStatusUpdate guards the Command Center tracker: a new analysis
// starts as a 'SAVED' application, and PATCH /analyses/{id}/application updates
// its status/deadline/notes, reflected back in GET /analyses.
func TestApplicationStatusUpdate(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t)
	register(t, c, ts, "tracker@example.com")
	id := createID(t, c, ts)

	// Fresh application defaults to SAVED.
	_, envList := doJSON(t, c, http.MethodGet, ts.URL+"/analyses?page=1&limit=10", nil)
	var items []store.AnalysisListItem
	_ = json.Unmarshal(envList.Data, &items)
	if len(items) != 1 || items[0].ApplicationStatus == nil || *items[0].ApplicationStatus != "SAVED" {
		t.Fatalf("expected 1 item with status SAVED, got %+v", items)
	}

	// Move it through the pipeline: APPLIED + deadline + notes.
	respU, envU := doJSON(t, c, http.MethodPatch, ts.URL+"/analyses/"+itoa(id)+"/application",
		map[string]any{"status": "APPLIED", "deadline": "2026-08-01", "notes": "Kirim via email HR"})
	if respU.StatusCode != http.StatusOK {
		t.Fatalf("update application status = %d, err=%s", respU.StatusCode, envU.Error.Code)
	}

	_, envList2 := doJSON(t, c, http.MethodGet, ts.URL+"/analyses?page=1&limit=10", nil)
	var items2 []store.AnalysisListItem
	_ = json.Unmarshal(envList2.Data, &items2)
	if len(items2) != 1 || items2[0].ApplicationStatus == nil || *items2[0].ApplicationStatus != "APPLIED" {
		t.Fatalf("expected status APPLIED after update, got %+v", items2)
	}
	if items2[0].Notes == nil || *items2[0].Notes != "Kirim via email HR" {
		t.Fatalf("expected notes to persist, got %+v", items2[0].Notes)
	}
	if items2[0].Deadline == nil {
		t.Fatal("expected deadline to persist")
	}

	// Invalid status is rejected.
	respBad, _ := doJSON(t, c, http.MethodPatch, ts.URL+"/analyses/"+itoa(id)+"/application",
		map[string]any{"status": "BOGUS"})
	if respBad.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid status, got %d", respBad.StatusCode)
	}
}

type cvLibItem struct {
	ID            int64  `json:"id"`
	FileName      string `json:"fileName"`
	Label         *string `json:"label"`
	AnalysisCount int    `json:"analysisCount"`
}

func listCVs(t *testing.T, c *http.Client, ts *httptest.Server) []cvLibItem {
	t.Helper()
	_, env := doJSON(t, c, http.MethodGet, ts.URL+"/cvs", nil)
	var items []cvLibItem
	_ = json.Unmarshal(env.Data, &items)
	return items
}

// TestCVLibraryAndReuse guards Command Center Fase 2: a saved CV shows in the
// library, can be reused for a new JD without re-uploading, and its usage count
// climbs. Reuse still consumes a trial credit.
func TestCVLibraryAndReuse(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t)
	register(t, c, ts, "library@example.com")

	// First upload → creates a CV and one analysis.
	id := createID(t, c, ts)
	pollUntilDone(t, c, ts, id)

	cvs := listCVs(t, c, ts)
	if len(cvs) != 1 || cvs[0].AnalysisCount != 1 {
		t.Fatalf("expected 1 CV used once, got %+v", cvs)
	}
	cvID := cvs[0].ID

	// Reuse that CV for a NEW job — no upload.
	resp, env := analyzeWithSavedCV(t, c, ts, longJD, cvID)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("reuse-cv analysis status = %d, err=%s", resp.StatusCode, env.Error.Code)
	}
	var created struct {
		AnalysisID     int64 `json:"analysisId"`
		TrialRemaining int   `json:"trialRemaining"`
	}
	_ = json.Unmarshal(env.Data, &created)
	if created.AnalysisID == 0 {
		t.Fatal("reuse-cv returned no analysisId")
	}
	// Started with 3 trials, used 2 → 1 left.
	if created.TrialRemaining != 1 {
		t.Fatalf("expected trialRemaining 1 after 2 analyses, got %d", created.TrialRemaining)
	}
	a := pollUntilDone(t, c, ts, created.AnalysisID)
	if a.Status != store.StatusDone {
		t.Fatalf("reuse-cv expected DONE, got %s (reason=%v)", a.Status, a.FailReason)
	}

	// Library still 1 CV, now used twice.
	cvs2 := listCVs(t, c, ts)
	if len(cvs2) != 1 || cvs2[0].AnalysisCount != 2 {
		t.Fatalf("expected 1 CV used twice, got %+v", cvs2)
	}

	// Reusing another user's cv id must 404.
	other := newClient(t)
	register(t, other, ts, "library-b@example.com")
	respX, _ := analyzeWithSavedCV(t, other, ts, longJD, cvID)
	if respX.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 reusing another user's CV, got %d", respX.StatusCode)
	}
}

// TestArchiveCV: deleting a saved CV soft-hides it from the library.
func TestArchiveCV(t *testing.T) {
	ts := newTestServer(t)
	c := newClient(t)
	register(t, c, ts, "archive@example.com")
	id := createID(t, c, ts)
	pollUntilDone(t, c, ts, id)

	cvs := listCVs(t, c, ts)
	if len(cvs) != 1 {
		t.Fatalf("expected 1 CV, got %d", len(cvs))
	}

	respD, _ := doJSON(t, c, http.MethodDelete, ts.URL+"/cvs/"+itoa(cvs[0].ID), nil)
	if respD.StatusCode != http.StatusOK {
		t.Fatalf("archive cv status = %d", respD.StatusCode)
	}
	if len(listCVs(t, c, ts)) != 0 {
		t.Fatal("expected empty library after archive")
	}
}
