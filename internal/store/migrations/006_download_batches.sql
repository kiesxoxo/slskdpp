-- Downloads queued together through the slskd-compatible batch API.
ALTER TABLE downloads ADD COLUMN batch TEXT NOT NULL DEFAULT '';

CREATE TABLE download_batches (
    id          TEXT    PRIMARY KEY,  -- a UUID chosen by the client
    username    TEXT    NOT NULL,
    external_id TEXT    NOT NULL DEFAULT '',
    destination TEXT    NOT NULL DEFAULT '',  -- folder under the downloads directory, or '' for the default
    created     INTEGER NOT NULL              -- unix ms
);
