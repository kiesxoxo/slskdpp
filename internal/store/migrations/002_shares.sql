-- Share roots added from the UI; roots from the config file are not stored.
CREATE TABLE share_roots (
    alias TEXT PRIMARY KEY,
    path  TEXT NOT NULL
);

CREATE TABLE shared_files (
    id     INTEGER PRIMARY KEY,
    root   TEXT    NOT NULL,  -- share alias
    folder TEXT    NOT NULL,  -- virtual folder: alias\sub\dir
    name   TEXT    NOT NULL,
    size   INTEGER NOT NULL,
    mtime  INTEGER NOT NULL   -- unix ns
);

CREATE UNIQUE INDEX shared_files_path ON shared_files (folder, name);
CREATE INDEX shared_files_root ON shared_files (root);

-- Word index over paths for answering searches; the text lives in
-- shared_files only.
CREATE VIRTUAL TABLE shared_fts USING fts5 (
    folder, name,
    content = 'shared_files', content_rowid = 'id',
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER shared_files_ai AFTER INSERT ON shared_files BEGIN
    INSERT INTO shared_fts (rowid, folder, name) VALUES (new.id, new.folder, new.name);
END;

CREATE TRIGGER shared_files_ad AFTER DELETE ON shared_files BEGIN
    INSERT INTO shared_fts (shared_fts, rowid, folder, name) VALUES ('delete', old.id, old.folder, old.name);
END;

CREATE TRIGGER shared_files_au AFTER UPDATE ON shared_files BEGIN
    INSERT INTO shared_fts (shared_fts, rowid, folder, name) VALUES ('delete', old.id, old.folder, old.name);
    INSERT INTO shared_fts (rowid, folder, name) VALUES (new.id, new.folder, new.name);
END;
