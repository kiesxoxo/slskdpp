# Changelog

## [Unreleased]

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
