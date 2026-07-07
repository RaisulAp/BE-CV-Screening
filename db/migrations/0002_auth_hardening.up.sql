-- Auth hardening: email verification + per-IP guest signup tracking, so
-- registering (or clearing cookies for a fresh guest batch) isn't a free,
-- frictionless way to keep burning paid OpenAI quota.

ALTER TABLE users
    ADD COLUMN email_verified_at      TIMESTAMPTZ,
    ADD COLUMN verification_token     TEXT,
    ADD COLUMN verification_expires_at TIMESTAMPTZ,
    ADD COLUMN signup_ip              TEXT;

-- Fast lookup when a user clicks the verification link.
CREATE INDEX idx_users_verification_token ON users(verification_token) WHERE verification_token IS NOT NULL;

-- Fast "how many guest accounts has this IP created in the last day" check.
CREATE INDEX idx_users_signup_ip_guest_created ON users(signup_ip, created_at) WHERE is_guest = true;
