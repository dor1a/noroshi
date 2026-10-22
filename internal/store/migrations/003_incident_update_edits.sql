-- Admins can correct an update's text after posting it. The page marks the
-- update as edited, and the edit is not announced again.
ALTER TABLE incident_update
    ADD COLUMN edited_at timestamptz,
    ADD COLUMN edited_by text;
