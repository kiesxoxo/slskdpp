-- slskd compatibility: download batches and the event log.

-- Downloads queued together through the slskd-compatible batch API.
ALTER TABLE downloads ADD COLUMN batch TEXT NOT NULL DEFAULT '';

CREATE TABLE download_batches (
    id          TEXT    PRIMARY KEY,  -- a UUID chosen by the client
    username    TEXT    NOT NULL,
    external_id TEXT    NOT NULL DEFAULT '',
    destination TEXT    NOT NULL DEFAULT '',  -- folder under the downloads directory, or '' for the default
    created     INTEGER NOT NULL              -- unix ms
);

-- slskd's event log, served at /api/v0/events.
CREATE TABLE compat_events (
    id        TEXT    PRIMARY KEY,  -- a UUID
    type      TEXT    NOT NULL,
    timestamp INTEGER NOT NULL,     -- unix microseconds, unique and increasing
    data      TEXT    NOT NULL      -- the event as JSON, without id, timestamp and type
);

CREATE INDEX compat_events_timestamp ON compat_events (timestamp);
