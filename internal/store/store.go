package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned by every getter when no row matches (typically an
// ownership-scoped query). Services translate this to a 404 AppError.
var ErrNotFound = errors.New("store: not found")

// Analysis status values (mirror the analysis_status enum).
const (
	StatusPending    = "PENDING"
	StatusProcessing = "PROCESSING"
	StatusDone       = "DONE"
	StatusFailed     = "FAILED"
)

type User struct {
	ID              int64      `json:"id"`
	Email           string     `json:"email"` // "" untuk guest
	PasswordHash    string     `json:"-"`
	IsGuest         bool       `json:"isGuest"`
	EmailVerifiedAt *time.Time `json:"emailVerifiedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// NewUserParams carries everything needed to create/upgrade a real (non-guest)
// account. VerificationToken/VerificationExpiresAt are set when email
// verification is enforced (RESEND_API_KEY configured); EmailVerifiedAt is set
// to "now" instead when verification is disabled, so registration keeps
// working with zero friction until email sending is configured.
type NewUserParams struct {
	Email                 string
	PasswordHash          string
	SignupIP              string
	VerificationToken     *string
	VerificationExpiresAt *time.Time
	EmailVerifiedAt       *time.Time
}

type JobDescription struct {
	ID         int64           `json:"id"`
	UserID     int64           `json:"-"`
	RawText    string          `json:"rawText"`
	Title      *string         `json:"title"`
	Company    *string         `json:"company"`
	ParsedJSON json.RawMessage `json:"parsedJson"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type Cv struct {
	ID                  int64           `json:"id"`
	UserID              int64           `json:"-"`
	FileName            string          `json:"fileName"`
	Label               *string         `json:"label"`
	Archived            bool            `json:"-"`
	RawText             *string         `json:"-"`
	ParsedJSON          json.RawMessage `json:"parsedJson"`
	StructureReportJSON json.RawMessage `json:"structureReportJson"`
	CreatedAt           time.Time       `json:"createdAt"`
}

// CvLibraryItem is a saved-CV card for the CV Library page: metadata + light
// analytics (how many times used, its most recent score).
type CvLibraryItem struct {
	ID            int64     `json:"id"`
	FileName      string    `json:"fileName"`
	Label         *string   `json:"label"`
	AnalysisCount int       `json:"analysisCount"`
	LastScore     *int32    `json:"lastScore"`
	CreatedAt     time.Time `json:"createdAt"`
}

type Analysis struct {
	ID                     int64           `json:"id"`
	UserID                 int64           `json:"-"`
	JobID                  int64           `json:"jobId"`
	CvID                   int64           `json:"cvId"`
	ParentAnalysisID       *int64          `json:"parentAnalysisId"`
	Status                 string          `json:"status"`
	ProgressStep           *string         `json:"progressStep"`
	FailReason             *string         `json:"failReason"`
	MatchScore             *int32          `json:"matchScore"`
	ScoreBreakdownJSON     json.RawMessage `json:"scoreBreakdownJson"`
	MatchedKeywordsJSON    json.RawMessage `json:"matchedKeywordsJson"`
	MissingKeywordsJSON    json.RawMessage `json:"missingKeywordsJson"`
	SkillGapJSON           json.RawMessage `json:"skillGapJson"`
	ExperienceGapJSON      json.RawMessage `json:"experienceGapJson"`
	RewriteSuggestionsJSON json.RawMessage `json:"rewriteSuggestionsJson"`
	AppliedRewritesJSON    json.RawMessage `json:"appliedRewritesJson"`
	CreatedAt              time.Time       `json:"createdAt"`
}

// AnalysisListItem is the flattened row for the history/tracker page. The
// application_* fields turn it into a job-application card (Command Center).
type AnalysisListItem struct {
	ID                int64      `json:"id"`
	JobTitle          *string    `json:"jobTitle"`
	Company           *string    `json:"company"`
	CvFileName        string     `json:"cvFileName"`
	MatchScore        *int32     `json:"matchScore"`
	LatestScore       *int32     `json:"latestScore"`
	Status            string     `json:"status"`
	ApplicationStatus *string    `json:"applicationStatus"`
	Deadline          *time.Time `json:"deadline"`
	Notes             *string    `json:"notes"`
	CreatedAt         time.Time  `json:"createdAt"`
}

// MatchResultInput carries every field produced by the /match step so the
// service can persist it in one call.
type MatchResultInput struct {
	Score         int32
	Breakdown     json.RawMessage
	Matched       json.RawMessage
	Missing       json.RawMessage
	SkillGap      json.RawMessage
	ExperienceGap json.RawMessage
}

// Store is the single persistence seam. Services depend on this interface, so
// the pgx implementation can be swapped (e.g. for sqlc or a fake in tests)
// without touching business logic (BACKEND.md §4/§4c).
type Store interface {
	// users
	CreateUser(ctx context.Context, p NewUserParams) (User, error)
	UpgradeGuestToUser(ctx context.Context, guestID int64, p NewUserParams) (User, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
	GetUserByID(ctx context.Context, id int64) (User, error)
	GetUserByVerificationToken(ctx context.Context, token string) (User, error)
	VerifyUserEmail(ctx context.Context, userID int64) error
	SetVerificationToken(ctx context.Context, userID int64, token string, expiresAt time.Time) error

	// billing (trial quota — see internal/store/billing.go)
	SeedTrial(ctx context.Context, userID int64) error
	GetTrialRemaining(ctx context.Context, userID int64) (int, error)
	ConsumeTrial(ctx context.Context, userID int64) (int, error)

	// job_descriptions
	CreateJob(ctx context.Context, userID int64, rawText string) (JobDescription, error)
	UpdateJobParsed(ctx context.Context, id int64, title, company *string, parsed json.RawMessage) error
	GetJob(ctx context.Context, id int64) (JobDescription, error)
	ListJobs(ctx context.Context, userID int64) ([]JobDescription, error)

	// cvs
	CreateCV(ctx context.Context, userID int64, fileName string) (Cv, error)
	UpdateCVParsed(ctx context.Context, id int64, rawText string, parsed, structure json.RawMessage) error
	GetCV(ctx context.Context, id int64) (Cv, error)
	GetCVForUser(ctx context.Context, id, userID int64) (Cv, error)
	ListCVLibrary(ctx context.Context, userID int64) ([]CvLibraryItem, error)
	RenameCV(ctx context.Context, id, userID int64, label string) error
	ArchiveCV(ctx context.Context, id, userID int64) error

	// analyses
	CreateAnalysis(ctx context.Context, userID, jobID, cvID int64, parentID *int64) (Analysis, error)
	GetAnalysis(ctx context.Context, id int64) (Analysis, error)
	GetAnalysisForUser(ctx context.Context, id, userID int64) (Analysis, error)
	UpdateAnalysisProgress(ctx context.Context, id int64, status string, step *string) error
	UpdateAnalysisResult(ctx context.Context, id int64, m MatchResultInput) error
	UpdateAnalysisRewrites(ctx context.Context, id int64, rewrites json.RawMessage) error
	SetAnalysisApplied(ctx context.Context, id int64, applied json.RawMessage) error
	MarkAnalysisFailed(ctx context.Context, id int64, reason string) error
	MarkAnalysisDone(ctx context.Context, id int64) error
	ListAnalyses(ctx context.Context, userID int64, limit, offset int) ([]AnalysisListItem, error)
	UpdateApplication(ctx context.Context, id, userID int64, status *string, deadline *time.Time, notes *string) error
	DeleteAnalysis(ctx context.Context, id, userID int64) error
	RecoverInterrupted(ctx context.Context) (int64, error)

	Close()
}
