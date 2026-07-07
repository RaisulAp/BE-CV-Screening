package httpx

import (
	"net/http"
)

// AppError is a domain error carrying a stable machine code (for the FE to
// switch on), a human message (Indonesian, shown to the user), and the HTTP
// status to emit. Katalog kode ada di BACKEND.md §6/§7/§9.
type AppError struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *AppError) Error() string { return e.Code + ": " + e.Message }

func newErr(status int, code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTPStatus: status}
}

// Generic errors.
func ErrValidation(msg string) *AppError {
	return newErr(http.StatusBadRequest, "VALIDATION_ERROR", msg)
}
func ErrUnauthorized(msg string) *AppError {
	return newErr(http.StatusUnauthorized, "UNAUTHORIZED", msg)
}
func ErrNotFound(msg string) *AppError { return newErr(http.StatusNotFound, "NOT_FOUND", msg) }
func ErrInternal() *AppError {
	return newErr(http.StatusInternalServerError, "INTERNAL", "Terjadi kesalahan di server. Coba lagi sebentar.")
}

// Domain-specific errors.
func ErrEmailTaken() *AppError {
	return newErr(http.StatusConflict, "EMAIL_TAKEN", "Email ini sudah terdaftar. Silakan masuk.")
}
func ErrInvalidCredentials() *AppError {
	return newErr(http.StatusUnauthorized, "INVALID_CREDENTIALS", "Email atau kata sandi salah.")
}
func ErrLoginRequired() *AppError {
	return newErr(http.StatusUnauthorized, "LOGIN_REQUIRED",
		"Masuk atau daftar dulu untuk mulai analisa CV-mu — gratis 3x percobaan setelah daftar.")
}
func ErrTrialExhausted() *AppError {
	return newErr(http.StatusPaymentRequired, "TRIAL_EXHAUSTED",
		"Trial gratismu (3x) sudah habis. Fitur berlangganan sedang kami siapkan — nantikan kabarnya!")
}
func ErrInvalidPDF() *AppError {
	return newErr(http.StatusBadRequest, "INVALID_PDF", "File harus berupa PDF yang valid.")
}
func ErrCVTooLarge(maxMB int64) *AppError {
	return &AppError{
		Code:       "CV_TOO_LARGE",
		Message:    "Ukuran CV melebihi batas. Maksimal file PDF adalah beberapa MB saja.",
		HTTPStatus: http.StatusRequestEntityTooLarge,
	}
}
func ErrJDTooShort() *AppError {
	return newErr(http.StatusBadRequest, "JD_TOO_SHORT",
		"Deskripsi lowongan terlalu pendek. Tempel seluruh teks lowongan (min. 100 karakter) agar hasilnya akurat.")
}
func ErrCVUnreadable() *AppError {
	return newErr(http.StatusUnprocessableEntity, "CV_UNREADABLE",
		"CV-mu tidak bisa dibaca. Coba export ulang ke PDF dari Word/Canva, bukan hasil scan.")
}
func ErrAIServiceDown() *AppError {
	return newErr(http.StatusServiceUnavailable, "AI_SERVICE_DOWN",
		"Mesin AI sedang tidak bisa dihubungi. Coba lagi beberapa saat lagi.")
}
func ErrAIBadOutput() *AppError {
	return newErr(http.StatusBadGateway, "AI_BAD_OUTPUT",
		"Mesin AI memberi jawaban yang tidak bisa diproses. Coba ulangi analisisnya.")
}
func ErrEmailNotVerified() *AppError {
	return newErr(http.StatusForbidden, "EMAIL_NOT_VERIFIED",
		"Verifikasi emailmu dulu (cek inbox/spam) sebelum membuat analisis baru.")
}
func ErrCaptchaFailed() *AppError {
	return newErr(http.StatusBadRequest, "CAPTCHA_FAILED",
		"Verifikasi captcha gagal. Coba lagi.")
}
func ErrInvalidVerificationToken() *AppError {
	return newErr(http.StatusBadRequest, "INVALID_VERIFICATION_TOKEN",
		"Link verifikasi tidak valid atau sudah kedaluwarsa. Minta link baru.")
}
