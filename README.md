# CV-Screening — Web Backend (Go)

Orkestrator + penjaga data untuk **"Teman Melamar Kerja"**. Menangani auth, penyimpanan data, dan pipeline analisis async — semua "kecerdasan AI" ada di AI Service (Python), backend ini hanya memanggilnya lewat HTTP. Mengikuti [`../Brain/BACKEND.md`](../Brain/BACKEND.md) dan [`../Brain/BLUEPRINT.md`](../Brain/BLUEPRINT.md).

---

## 1. Prasyarat

- **Go 1.25+** (cek: `go version`).
- **PostgreSQL** jalan dan bisa diakses (versi 14+ cukup — skema saat ini belum memakai ekstensi `pgvector`, jadi image Postgres biasa juga cukup, bukan wajib `pgvector/pgvector`).
- (Opsional untuk AI sungguhan) **AI Service** sudah jalan — lihat [`../AIService/README.md`](../AIService/README.md). Tidak wajib untuk development BE+FE — lihat §4 Mode Mock AI.

---

## 2. Setup Database (sekali saja)

Pilih salah satu:

**Opsi A — Postgres sudah terpasang native di komputer:**
```powershell
# Login ke psql lalu buat database
psql -U postgres -c "CREATE DATABASE cvscreening;"
```

**Opsi B — pakai Docker (tidak perlu install Postgres):**
```powershell
docker run -d --name cvscreening-db `
  -e POSTGRES_PASSWORD=password `
  -e POSTGRES_DB=cvscreening `
  -p 5432:5432 `
  postgres:16
```

Migrasi skema **tidak perlu dijalankan manual** — BE menjalankannya otomatis (dan idempotent) setiap kali `go run ./cmd/server` start, dari file SQL di `db/migrations/*.sql` yang sudah di-embed ke binary.

---

## 3. Setup & Menjalankan

```powershell
cd BE

# 1. Salin env dan sesuaikan
copy .env.example .env
notepad .env
```

Minimal yang perlu disesuaikan di `.env`:
- `DATABASE_URL` — cocokkan user/password/nama-db dengan setup Postgres-mu di §2.
- `JWT_SECRET` — ganti dengan string acak panjang (jangan pakai contoh apa adanya untuk production).

```powershell
# 2. Download dependency Go (otomatis juga saat `go run`, tapi bisa dijalankan eksplisit)
go mod download

# 3. Jalankan (migrasi otomatis dijalankan saat boot)
go run ./cmd/server
```

Server listen di `http://localhost:3001` (atau sesuai `PORT` di `.env`).

### Cek server hidup
```powershell
curl http://localhost:3001/health
# -> {"success":true,"data":{"status":"ok"}}
```

---

## 4. Mode Mock AI (penting untuk development tanpa AI Service)

`.env` punya `AI_MOCK`:
- **`AI_MOCK=true`** → BE memakai jawaban AI dummy yang deterministik (`internal/aiclient/mock.go`). **Seluruh BE & FE bisa dikembangkan/diuji tanpa AI Service (Python) sama sekali** — ini mode default yang disarankan untuk kerja sehari-hari di FE/BE.
- **`AI_MOCK=false`** → BE memanggil AI Service sungguhan lewat HTTP ke `AI_SERVICE_URL`. Pastikan AI Service sudah jalan dulu (lihat [`../AIService/README.md`](../AIService/README.md)), kalau tidak semua analisis akan gagal dengan `fail_reason: AI_SERVICE_DOWN`.

---

## 5. Konfigurasi (`.env`)

| Variabel | Default | Keterangan |
|---|---|---|
| `DATABASE_URL` | *(wajib)* | Connection string Postgres, format `postgres://user:pass@host:port/db?sslmode=disable` |
| `JWT_SECRET` | *(wajib)* | Secret untuk sign JWT — string acak panjang |
| `JWT_EXPIRES_IN` | `168h` | Masa berlaku token/cookie login (168h = 7 hari) |
| `AI_SERVICE_URL` | `http://localhost:8000` | Alamat AI Service — **harus cocok dengan port tempat `uvicorn` dijalankan** |
| `AI_MOCK` | `false` | `true` = pakai jawaban AI dummy (lihat §4), `false` = panggil AI Service sungguhan |
| `FRONTEND_URL` | `http://localhost:3000` | Dipakai untuk konfigurasi CORS |
| `PORT` | `3001` | Port HTTP server BE |
| `MAX_CV_SIZE_MB` | `5` | Batas ukuran file CV yang diterima |
| `COOKIE_SECURE` | `false` | Set `true` di production (HTTPS) — biarkan `false` untuk localhost |
| `DEMO_ENABLED` | `false` | `true` = mount route `/demo/*` (lihat §5a) — tanpa login, tanpa simpan ke DB |

---

## 5a. Demo routes (tanpa login)

Untuk coba-coba/demo langsung ke teman tanpa lewat FE/login, set `DEMO_ENABLED=true` di `.env`, lalu pakai:

| Route | Body | Keterangan |
|---|---|---|
| `POST /demo/analyze-jd` | JSON `{"text": "..."}` | Panggil `AnalyzeJD` saja |
| `POST /demo/parse-cv` | multipart, field `cvFile` (PDF) | Panggil `ParseCV` saja |
| `POST /demo/match` | multipart, field `jobText` + `cvFile` | Full pipeline sinkron: JD → parse CV → match → rewrite tersaran, langsung dibalas dalam 1 response (tanpa polling) |

Semua route ini ada di `internal/demo/handler.go`, langsung memanggil `aiclient.Client` yang sama dengan pipeline utama — **tidak menyentuh database sama sekali** dan **tidak ada pengecekan login**. Dibatasi 20 request/jam per IP. Matikan lagi (`DEMO_ENABLED=false`, default) kalau sudah selesai demo/testing, terutama sebelum deploy ke tempat yang bisa diakses publik.

---

## 6. Testing

```powershell
go test ./...
```

Test e2e memakai database `cvscreening_test` (atau set `TEST_DATABASE_URL` untuk menunjuk ke database test terpisah). Mencakup: auth, alur analisis penuh (poll→DONE), rescore (Momen D, honesty check), riwayat, rewrite per-bullet, export, serta kasus gagal (bukan PDF, JD terlalu pendek, tanpa auth, akses milik user lain → 404).

---

## 7. API singkat

Semua endpoint memakai cookie `token` (httpOnly) untuk identitas — termasuk guest (dibuat otomatis saat analisis pertama, tanpa perlu register). Envelope response seragam: `{ success, data?, error? }`.

| Endpoint | Keterangan |
|---|---|
| `POST /auth/register`, `POST /auth/login`, `POST /auth/logout`, `GET /auth/me` | Auth |
| `POST /analyses` | Buat analisis baru (multipart: `jobText` + `cvFile`) → `202 { analysisId, status: PENDING }` |
| `GET /analyses/{id}` | Ambil hasil (FE polling tiap ~2 detik sampai `status: DONE`) |
| `POST /analyses/{id}/rescore` | Momen D — kirim `appliedRewrites[]`, buat analisis anak yang re-match |
| `GET /analyses` | Riwayat lamaran (list) |
| `DELETE /analyses/{id}` | Hapus 1 analisis |
| `GET /analyses/{id}/export` | Export laporan |
| `POST /rewrites` | Rewrite 1 bullet on-demand (Momen B) |
| `GET /jobs`, `GET /cvs` | Katalog JD & CV tersimpan |

Guest (belum login) dapat **3 analisis gratis**; analisis ke-4 butuh login (`LOGIN_REQUIRED`).

---

## 8. Catatan implementasi (vs BACKEND.md)

Dua penyesuaian sadar, tetap dalam koridor dokumen:

1. **Store pakai pgx hand-written**, bukan sqlc. Dokumen §4c mengizinkan implementasi `Store` alternatif; keduanya di balik interface `store.Store` yang sama, jadi service/handler tak berubah.
2. **Migrasi dijalankan runner embedded** (`internal/store/db.go`), bukan CLI `golang-migrate`. File `.sql` tetap di `db/migrations/` persis seperti dokumen — di-embed lewat `db/migrations.go` agar ikut ke dalam single binary.

## 9. Peta paket

| Paket | Isi |
|---|---|
| `cmd/server` | entrypoint: config, DB, worker, http.Server, graceful shutdown |
| `internal/config` | Config typed dari ENV |
| `internal/store` | interface `Store` + implementasi pgx + migrator |
| `internal/httpx` | envelope response seragam + katalog AppError |
| `internal/auth` | JWT, bcrypt, register/login/me + middleware |
| `internal/aiclient` | kontrak ke AI Service — `HTTPClient` (asli) & `MockClient` |
| `internal/analyses` | pipeline worker async + service + handler (jalur utama produk) |
| `internal/catalog` | list JD & CV tersimpan |
| `internal/server` | router chi, middleware, http server |

---

## 10. Troubleshooting

| Gejala | Penyebab | Solusi |
|---|---|---|
| `dial tcp ... connect: connection refused` saat start | Postgres belum jalan / `DATABASE_URL` salah | Cek Postgres aktif (`docker ps` atau service Postgres native), cek connection string |
| Analisis selalu `FAILED` dengan `AI_SERVICE_DOWN` | `AI_MOCK=false` tapi AI Service belum jalan | Jalankan AI Service dulu, atau set `AI_MOCK=true` untuk development tanpa AI |
| `EMAIL_TAKEN` / `INVALID_CREDENTIALS` tak terduga | Data lama di database dev | Wajar kalau sudah pernah register sebelumnya; pakai email lain atau reset database dev |
| Port 3001 sudah dipakai | Proses lain pegang port itu | Ubah `PORT` di `.env`, sesuaikan juga `NEXT_PUBLIC_API_URL` di `FE/.env.local` |

---

## 11. Menjalankan bersama AI Service & FE (urutan startup)

```
1. PostgreSQL     (native service / docker run, lihat §2)
2. AI Service     uvicorn app.main:app --port 8000     (../AIService, opsional kalau AI_MOCK=true)
3. Web Backend    go run ./cmd/server                   (folder ini)
4. Frontend       npm run dev                            (../FE)
```
Buka `http://localhost:3000` untuk mulai memakai aplikasi. Detail service lain: [`../AIService/README.md`](../AIService/README.md), [`../FE/README.md`](../FE/README.md).
