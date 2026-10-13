-- Structure synced from the YAML config. Rows are never deleted when they
-- disappear from the config; archived_at is set so history stays intact.
CREATE TABLE page (
    id          bigserial PRIMARY KEY,
    key         text NOT NULL UNIQUE,
    title       jsonb NOT NULL,
    timezone    text NOT NULL,
    position    int NOT NULL DEFAULT 0,
    archived_at timestamptz
);

CREATE TABLE component_group (
    id          bigserial PRIMARY KEY,
    page_id     bigint NOT NULL REFERENCES page (id),
    key         text NOT NULL,
    name        jsonb NOT NULL,
    position    int NOT NULL DEFAULT 0,
    archived_at timestamptz,
    UNIQUE (page_id, key)
);

CREATE TABLE component (
    id          bigserial PRIMARY KEY,
    page_id     bigint NOT NULL REFERENCES page (id),
    group_id    bigint NOT NULL REFERENCES component_group (id),
    key         text NOT NULL,
    name        jsonb NOT NULL,
    description jsonb NOT NULL DEFAULT '{}'::jsonb,
    position    int NOT NULL DEFAULT 0,
    archived_at timestamptz,
    UNIQUE (page_id, key)
);

CREATE TABLE checks (
    id           bigserial PRIMARY KEY,
    component_id bigint NOT NULL REFERENCES component (id),
    key          text NOT NULL,
    type         text NOT NULL,
    spec         jsonb NOT NULL,
    position     int NOT NULL DEFAULT 0,
    archived_at  timestamptz,
    UNIQUE (component_id, key)
);

-- Raw probe results, pruned after a retention period.
CREATE TABLE check_result (
    check_id   bigint NOT NULL REFERENCES checks (id),
    at         timestamptz NOT NULL,
    ok         boolean NOT NULL,
    slow       boolean NOT NULL DEFAULT false,
    skipped    boolean NOT NULL DEFAULT false,
    latency_ms int NOT NULL DEFAULT 0,
    detail     text NOT NULL DEFAULT ''
);
CREATE INDEX check_result_check_at ON check_result (check_id, at DESC);
CREATE INDEX check_result_at ON check_result (at);

-- Current debounced state per check.
CREATE TABLE check_state (
    check_id         bigint PRIMARY KEY REFERENCES checks (id),
    state            text NOT NULL,
    degraded         boolean NOT NULL DEFAULT false,
    since            timestamptz NOT NULL,
    consecutive_fail int NOT NULL DEFAULT 0,
    consecutive_ok   int NOT NULL DEFAULT 0,
    last_at          timestamptz,
    last_ok          boolean,
    last_latency_ms  int,
    last_detail      text NOT NULL DEFAULT '',
    cert_expiry      timestamptz,
    domain_expiry    timestamptz
);

-- Component status changes. Kept forever; uptime is computed from these.
CREATE TABLE component_status_event (
    id           bigserial PRIMARY KEY,
    component_id bigint NOT NULL REFERENCES component (id),
    status       text NOT NULL,
    at           timestamptz NOT NULL,
    source       text NOT NULL
);
CREATE INDEX component_status_event_component_at ON component_status_event (component_id, at);

-- Periods when a prober was running. Time outside them counts as no data.
CREATE TABLE prober_session (
    id           bigserial PRIMARY KEY,
    instance     text NOT NULL,
    started_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL
);
CREATE INDEX prober_session_last_seen ON prober_session (last_seen_at);
