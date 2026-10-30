-- Audio details of shared files, sent with search and browse replies.
ALTER TABLE shared_files ADD COLUMN bitrate     INTEGER NOT NULL DEFAULT 0; -- kbps
ALTER TABLE shared_files ADD COLUMN duration    INTEGER NOT NULL DEFAULT 0; -- seconds
ALTER TABLE shared_files ADD COLUMN sample_rate INTEGER NOT NULL DEFAULT 0; -- Hz
ALTER TABLE shared_files ADD COLUMN bit_depth   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shared_files ADD COLUMN vbr         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shared_files ADD COLUMN lossless    INTEGER NOT NULL DEFAULT 0;

-- Files indexed before had no details: make the next scan read them.
UPDATE shared_files SET mtime = 0;
