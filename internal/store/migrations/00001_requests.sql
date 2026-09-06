-- +goose Up
-- One row per billed API attempt. The primary key is the identity Claude Code
-- gave the attempt (session, message, iteration), so re-collecting the same
-- transcript updates in place instead of duplicating: transcripts are appended
-- to while a session runs, and collect is expected to be run repeatedly.
CREATE TABLE requests (
    session_id     TEXT    NOT NULL,
    message_id     TEXT    NOT NULL,
    iteration      INTEGER NOT NULL,
    config_dir     TEXT    NOT NULL,
    project        TEXT    NOT NULL,
    request_id     TEXT    NOT NULL DEFAULT '',
    ts             DATETIME NOT NULL,
    model          TEXT    NOT NULL,
    effort         TEXT    NOT NULL DEFAULT '',
    version        TEXT    NOT NULL DEFAULT '',
    git_branch     TEXT    NOT NULL DEFAULT '',
    is_sidechain   BOOLEAN NOT NULL DEFAULT 0,
    input_tokens          INTEGER NOT NULL DEFAULT 0,
    output_tokens         INTEGER NOT NULL DEFAULT 0,
    thinking_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_5m_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
    -- context_tokens is input + cache_read + cache_creation, stored rather than
    -- computed because every report groups and filters on it and it is the one
    -- quantity the autocompact window bounds.
    context_tokens INTEGER NOT NULL DEFAULT 0,
    input_usd       REAL NOT NULL DEFAULT 0,
    output_usd      REAL NOT NULL DEFAULT 0,
    cache_read_usd  REAL NOT NULL DEFAULT 0,
    cache_write_usd REAL NOT NULL DEFAULT 0,
    priced          BOOLEAN NOT NULL DEFAULT 0,
    price_verified  BOOLEAN NOT NULL DEFAULT 0,
    assumed_ttl_tokens INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (session_id, message_id, iteration)
);

CREATE INDEX idx_requests_ts ON requests (ts);
CREATE INDEX idx_requests_session ON requests (session_id);

-- One row per session. peak_context is how the autocompact window is observed:
-- the flag is passed at launch and never written to the transcript, but a
-- session cannot exceed its window, so the peak is the ceiling made visible.
CREATE TABLE sessions (
    session_id   TEXT PRIMARY KEY,
    config_dir   TEXT NOT NULL,
    project      TEXT NOT NULL,
    started_at   DATETIME NOT NULL,
    ended_at     DATETIME NOT NULL,
    requests     INTEGER NOT NULL DEFAULT 0,
    human_turns  INTEGER NOT NULL DEFAULT 0,
    compactions  INTEGER NOT NULL DEFAULT 0,
    peak_context INTEGER NOT NULL DEFAULT 0,
    -- Claude Code's own accounting, present in a minority of transcripts.
    -- cost_state_present must be read before any of the columns below it: a
    -- real session can legitimately have cost zero, so zero is not absence.
    cost_state_present  BOOLEAN NOT NULL DEFAULT 0,
    cc_total_cost_usd   REAL NOT NULL DEFAULT 0,
    cc_api_duration_ms  INTEGER NOT NULL DEFAULT 0,
    cc_tool_duration_ms INTEGER NOT NULL DEFAULT 0,
    cc_wall_duration_ms INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_sessions_started ON sessions (started_at);

-- +goose Down
DROP TABLE sessions;
DROP TABLE requests;
