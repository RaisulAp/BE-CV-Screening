-- Command Center Fase 1 — Application Tracker.
-- Turns each root analysis into a trackable job application: a status through
-- the hunt pipeline (Disimpan → Dilamar → Interview → Offer/Ditolak), an
-- optional deadline, and free-form notes. Kept as columns on the root analysis
-- (not a separate applications table) because one root analysis already = one
-- application, and ListAnalyses already joins job+cv — so /history upgrades
-- into a tracker with no duplicate entity.

CREATE TYPE application_status AS ENUM ('SAVED','APPLIED','INTERVIEW','OFFER','REJECTED');

ALTER TABLE analyses
    ADD COLUMN application_status application_status,  -- NULL for child (rescore) rows
    ADD COLUMN deadline           DATE,
    ADD COLUMN notes              TEXT;

-- Backfill: every existing root analysis becomes a 'SAVED' application.
UPDATE analyses SET application_status = 'SAVED' WHERE parent_analysis_id IS NULL;
