CREATE TABLE downloads (
    id           INTEGER PRIMARY KEY,
    username     TEXT    NOT NULL,
    filename     TEXT    NOT NULL,  -- remote path
    size         INTEGER NOT NULL,
    dest         TEXT    NOT NULL,  -- path under the downloads directory
    state        TEXT    NOT NULL,
    bytes        INTEGER NOT NULL DEFAULT 0,
    attempts     INTEGER NOT NULL DEFAULT 0,
    error        TEXT    NOT NULL DEFAULT '',
    tried_users  TEXT    NOT NULL DEFAULT '',  -- newline-separated earlier sources
    next_attempt INTEGER NOT NULL DEFAULT 0,   -- unix ms
    created      INTEGER NOT NULL,             -- unix ms
    updated      INTEGER NOT NULL,
    completed    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX downloads_user_file ON downloads (username, filename);
