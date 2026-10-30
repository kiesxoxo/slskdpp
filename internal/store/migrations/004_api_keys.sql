CREATE TABLE api_keys (
    id        INTEGER PRIMARY KEY,
    name      TEXT    NOT NULL,
    hash      TEXT    NOT NULL UNIQUE, -- sha256 of the key, hex
    prefix    TEXT    NOT NULL,        -- start of the key, to tell keys apart
    created   INTEGER NOT NULL,        -- unix ms
    last_used INTEGER NOT NULL DEFAULT 0
);
