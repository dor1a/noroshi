-- A maintenance is planned ahead. Its started_at and resolved_at hold the
-- window and may lie in the future, and its components are declared under
-- maintenance for that window, so the status and uptime follow on their own.
ALTER TABLE incident
    ADD COLUMN kind text NOT NULL DEFAULT 'incident';
