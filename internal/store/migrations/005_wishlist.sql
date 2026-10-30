CREATE TABLE wishlist (
    id             INTEGER PRIMARY KEY,
    query          TEXT    NOT NULL,
    formats        TEXT    NOT NULL DEFAULT '',  -- comma-separated
    min_bitrate    INTEGER NOT NULL DEFAULT 0,
    free_slot_only INTEGER NOT NULL DEFAULT 0,
    min_files      INTEGER NOT NULL DEFAULT 0,
    auto_download  INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1,
    created        INTEGER NOT NULL,             -- unix ms
    last_run       INTEGER NOT NULL DEFAULT 0,
    last_search    INTEGER NOT NULL DEFAULT 0,   -- search token, while kept
    matches        INTEGER NOT NULL DEFAULT 0,   -- albums that passed the filter
    got_user       TEXT    NOT NULL DEFAULT '',  -- what auto-download fetched
    got_folder     TEXT    NOT NULL DEFAULT '',
    got_at         INTEGER NOT NULL DEFAULT 0
);
