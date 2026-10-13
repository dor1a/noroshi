-- Incidents are written by admins on the admin listener.
CREATE TABLE incident (
    id          bigserial PRIMARY KEY,
    page_id     bigint NOT NULL REFERENCES page (id),
    title       text NOT NULL,
    stage       text NOT NULL,
    started_at  timestamptz NOT NULL,
    resolved_at timestamptz,
    created_by  text NOT NULL
);
CREATE INDEX incident_page_started ON incident (page_id, started_at DESC);

CREATE TABLE incident_update (
    id          bigserial PRIMARY KEY,
    incident_id bigint NOT NULL REFERENCES incident (id) ON DELETE CASCADE,
    stage       text NOT NULL,
    body        text NOT NULL,
    at          timestamptz NOT NULL,
    author      text NOT NULL
);
CREATE INDEX incident_update_incident_at ON incident_update (incident_id, at);

-- Component statuses declared by an incident, as intervals. While until is
-- null the status overrides what the checks say, and uptime replays them.
CREATE TABLE incident_component (
    id           bigserial PRIMARY KEY,
    incident_id  bigint NOT NULL REFERENCES incident (id) ON DELETE CASCADE,
    component_id bigint NOT NULL REFERENCES component (id),
    status       text NOT NULL,
    since        timestamptz NOT NULL,
    until        timestamptz
);
CREATE INDEX incident_component_component_since ON incident_component (component_id, since);

-- Delivery results of incident announcements. Webhook URLs are not stored.
CREATE TABLE notification (
    id        bigserial PRIMARY KEY,
    update_id bigint NOT NULL REFERENCES incident_update (id) ON DELETE CASCADE,
    notifier  text NOT NULL,
    ok        boolean NOT NULL,
    attempts  int NOT NULL,
    detail    text NOT NULL DEFAULT '',
    at        timestamptz NOT NULL
);
CREATE INDEX notification_update ON notification (update_id);
