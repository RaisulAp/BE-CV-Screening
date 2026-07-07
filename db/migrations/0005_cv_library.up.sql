-- Command Center Fase 2 — CV Library (Career Profile).
-- Lets a user name their saved CVs and reuse them for new analyses instead of
-- re-uploading every time (the core "come back for job #2" retention lever).
-- Only metadata; the parsed CV text already persists in cvs.parsed_json (the
-- PDF bytes are still never stored — privacy stance unchanged, BLUEPRINT §3b).

ALTER TABLE cvs
    ADD COLUMN label    TEXT,                          -- human name; falls back to file_name in UI
    ADD COLUMN archived BOOLEAN NOT NULL DEFAULT false; -- soft-hide from the library
