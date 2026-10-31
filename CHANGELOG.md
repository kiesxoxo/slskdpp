# Changelog

## [Unreleased]

### Added

- slskd's login for API clients: `POST /api/v0/session` trades the web
  username and password for a token (a JWT valid for 7 days) to send as
  `Authorization: Bearer <token>`. `GET /api/v0/session` checks a token and
  `GET /api/v0/session/enabled` reports that login is required. Failed logins
  count towards the same lockout as the web UI's.
- slskd's files API: list the downloads and incomplete folders
  (`/api/v0/files/{downloads,incomplete}/directories[/{base64 path}]`,
  optionally recursive) and delete files and folders in them. Deleting is off
  unless the new `web.remote_file_management` option is set, as in slskd.
- slskd's application API: the full application state at
  `GET /api/v0/application`, `version/latest`, `gc`, `loopback`, and restart
  (`PUT`) and shutdown (`DELETE`), which need a login rather than an API key.
- slskd's options API: `GET /api/v0/options` and `/options/startup` show the
  config as slskd's options with secrets redacted; `PATCH /api/v0/options`
  moves the Soulseek listen address and port without a restart; and
  `/options/yaml` reads, checks and rewrites the config file (keeping a
  `.bak`). Changing anything needs the new `web.remote_configuration` option,
  off by default as in slskd.
- slskd's logs API: `GET /api/v0/logs/live` returns the latest 100 log
  records, and `/api/v0/logs/files` lists and serves the log files.
- Log files: the log is also written to daily files in `<data>/logs`, kept
  for 7 days. New `log_files` and `log_keep_days` options control this.

### Fixed

- Rescan on the Shares page, and searching a wishlist entry now, failed with
  "Unexpected end of JSON input".
- A rescan shows as scanning as soon as it is asked for, so the Shares page
  follows it, and slskd's `PUT /api/v0/shares` refuses a second one at once.

## [0.2.0] - 2026-10-31

### Web UI

- Search results are a sortable table showing, for each album, its quality
  (format with bit depth and sample rate, or bitrate), track count, size, total
  length, user and other sources, upload speed and slot or queue.
- Albums open in place to show every source, the folder path and the
  tracklist. Tick files to download only those, or expand all results at once.
- A text filter with `-word` exclusions, quality tiers (hi-res, lossless,
  320/256/192 kbps), and a switch to list every user's folder separately
  instead of grouping albums. Users can be hidden from a result.
- Search results and Browse mark files already queued or downloaded.
- Keyboard shortcuts on the search page, and a list of recent searches when
  no search is open.
- Downloads can be filtered by state and show time left.
- Toasts confirm queued downloads; a banner shows when the Soulseek server
  connection is down; the sidebar shows live transfer speeds.
- The Inter typeface is bundled, so the UI looks the same everywhere.

### Added

- slskd-compatible event log, `GET /api/v0/events`, so Lidarr's Tubifarry
  plugin can find finished files and stops logging "Failed to update download
  items from Slskd" every few seconds. It records `DownloadFileComplete`,
  `DownloadDirectoryComplete`, `UploadFileComplete`, `SoulseekClientConnected`
  and `SoulseekClientDisconnected` events, kept in the database.
- `POST /api/v0/events/{type}` raises a sample event of any slskd type.
- More of slskd's API: uploads (`/api/v0/transfers/uploads`, listing,
  cancelling, clearing), a download's place in the remote queue
  (`…/{id}/position`), user info, status, endpoint and browse progress
  (`/api/v0/users/{username}/…`), the server connection (`GET`, `PUT` to
  connect, `DELETE` to disconnect `/api/v0/server`), and shares (`GET
  /api/v0/shares`, `/shares/{id}`, `/shares/contents`, `/shares/{id}/contents`,
  `PUT` to rescan, `DELETE` to cancel a scan).

### Changed

- slskd-compatible transfer endpoints answer 400, as slskd does, when the ID
  is not a GUID.
- slskd-compatible download batches, `POST /api/v0/transfers/downloads/batches`
  and `GET /api/v0/transfers/downloads/batches/{id}`, honouring
  `options.destination` and reporting per-file `failures`.
- `batchId`, `startedAt`, `elapsedTime` and `remainingTime` on slskd-compatible
  transfers, and `transfers.download.destination.subdirectory` in
  `/api/v0/options`.

### Packaging

- Container images hold the same binaries as the release archives, and are
  smoke-tested before they get their version tags.

## [0.1.0] - 2026-10-31

The first release. slskdpp is young: it is well covered by tests and has run
against a simulated network, but has had little time on the real Soulseek
network yet. Please report what breaks.

### Soulseek client

- Logs in, stays connected and reconnects with backoff.
- Reaches other users directly, or through the server when they are
  firewalled.
- Joins the distributed search network as a leaf, so other users' searches
  reach your shares.

### Search and browse

- Results stream in, grouped into albums across users, and ranked so free
  slots, short queues and fast peers come first.
- Filters for format, bitrate, free slots, file count and excluded users.
- One click downloads a whole folder, fetching its full listing first.
- Browse any user's share as a filterable folder tree.

### Downloads

- The queue lives in SQLite and survives restarts. Partial files resume.
- Failures retry with backoff. When a source refuses a file or stays offline,
  slskdpp searches for the same file elsewhere and moves the rest of the
  folder to the user who has it.
- Each file is checked against its advertised size before it is moved into
  place.
- Global and per-download speed limits.

### Sharing and uploads

- Shared folders are indexed in SQLite with full-text search and kept up to
  date as files change.
- Audio details (bitrate, length, sample rate, bit depth) are read from MP3,
  FLAC, Ogg/Opus, M4A/ALAC, WAV and AIFF files.
- Upload slots, a fair queue across users, and an upload speed limit.

### Automation

- REST API under `/api/v1` with API keys, and live updates over server-sent
  events.
- Wishlist: saved searches that run on a schedule and can download the best
  match.
- Shell hooks and webhooks when a file or folder finishes.
- An slskd-compatible API under `/api/v0` for soularr and similar tools.

### Web UI

- Search, Browse, Downloads, Uploads, Wishlist, Shares and Settings.
- Works on phones, with light and dark themes.

### Security

- Peer input is size-capped by message type. Unrequested responses are
  dropped unread, inbound connections are limited, and the protocol decoders
  are fuzz-tested.
- Web login with rate limiting, signed session cookies, hashed API keys and a
  strict content security policy. Reverse proxies can be trusted for client
  addresses.

### Packaging

- One static binary with the UI built in, for Linux (amd64, arm64, armv7),
  macOS, Windows and FreeBSD. Only Linux has been tested; the others are
  built but untested.
- Container images for amd64, arm64 and armv7 on ghcr.io. The default image
  is distroless; the `-alpine` variant has a shell for hook commands.
- A hardened systemd unit.

### Not yet supported

Chat, private messages and rooms. Acting as a distributed parent. Automatic
port forwarding (UPnP/NAT-PMP). Blocking users from your uploads.
