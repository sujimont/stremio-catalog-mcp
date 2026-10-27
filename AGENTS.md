# Stremio AI Agent MCP Instructions

This repository is an autonomous **Stremio & Debrid AI Agent MCP Server** (`sujimont/stremio-catalog-mcp`), deployed as a stateless Streamable HTTP Go Function on Vercel.

### Core Architecture & Capabilities
1. **Catalog & Content Management**:
   - `catalogs_get` and `catalogs_update`: Optimistic CAS-protected custom catalog rows stored in Upstash Redis. Supports multi-profile isolation.
2. **Home Screen Presentation**:
   - `home_layout_get` and `home_layout_update`: Batch hide/show and reorder catalog rows and installed addons on Stremio's Home screen with pre-write backups.
3. **Library & Watch Progress**:
   - `watch_history_get`: Reads recent watch history, completion states, and progress percentages for spoiler-safe recommendation feeds.
4. **Stream Availability Pre-checker**:
   - `stream_availability_check`: Verifies stream health, seeders, quality (4K/1080p), and TorBox cloud cache status for movies and series episodes (`tt...:season:episode`).
5. **Deep Discovery & Regional Cinema**:
   - `tmdb_discover` and `tmdb_similar`: Discovers regional cinema (Tamil, Malayalam, Hindi, Korean, etc.), OTT provider streams, and similar titles.
6. **Autonomous Crawling & TorBox Ingestion**:
   - `media_crawl_and_ingest`: Accepts direct magnets or crawls torrent forum pages (e.g., 1TamilMV topics), enforces max size limits (default 10 GB), and auto-ingests cached releases to TorBox.
   - `stream_unmask`: Resolves masked embed players into direct video streams with optional TorBox WebDL ingestion.
7. **Addon Management**:
   - `addons_manage`: Chat-driven addon listing, manifest URL installation, and safe uninstallation.
8. **Smart TV & Home Assistant Integration**:
   - `play_on_tv` and `devices_manage`: Generates Stremio deep links and dispatches playback webhooks to Home Assistant or Android TV bridges.

---

### Security & Operational Rules

1. **SSRF Guard**:
   - Model-supplied external URLs (`page_url`, `url`, ad-hoc `webhook_url` tool arguments) MUST be HTTPS and are validated using `httputil.ValidatePublicURL` before fetching.
   - All outbound external requests use `httputil.NewSafeClient`, which blocks private, loopback, link-local (cloud metadata `169.254.169.254`), Carrier-Grade NAT (`100.64.0.0/10`), and multicast IP addresses, including across redirects.
   - Operator-configured webhooks (`TV_WEBHOOK_URL` and registered devices) are trusted to allow local Home Assistant bridges without false-positive SSRF blocks.
2. **Profile & Key Sanitization**:
   - Profile names are strictly sanitized with `NormalizeProfile` (allowing only `[a-z0-9_-]`) to prevent Redis key injection and namespace pollution.
3. **Input Validation**:
   - IMDb IDs must strictly validate against `^tt\d+(:\d+:\d+)?$`.
   - Media types must be strictly validated as `"movie"` or `"series"`.
   - IP addresses must validate with `net.ParseIP` and reject shell metacharacters.
3. **Secret Hygiene**:
   - Secrets (`TORBOX_API_KEY`, `TMDB_API_KEY`, `STREMIO_AUTH_KEY`, `MCP_API_KEY`) MUST be stored in server environment variables only.
   - Never accept secrets as tool arguments in prompt context. Never extract keys from installed addon transport URLs.
4. **Honest Tool Annotations**:
   - Informational/query tools (`catalogs_get`, `home_layout_get`, `watch_history_get`, `stream_availability_check`, `tmdb_discover`, `tmdb_similar`) advertise `annotations.readOnlyHint: true`.
   - Mutating tools (`catalogs_update`, `home_layout_update`, `media_crawl_and_ingest`, `stream_unmask`, `play_on_tv`, `addons_manage`, `devices_manage`) advertise `annotations.readOnlyHint: false`. Destructive operations set `destructiveHint: true`.
   - Omit `openWorldHint`.
5. **Authentication**:
   - MCP authentication supports `Authorization: Bearer <key>` header (recommended) or `?key=<key>` query parameter. Missing keys fail closed with HTTP 503. Invalid keys return HTTP 401.
6. **Vercel Statelessness**:
   - All state resides in Redis or the user's Stremio account. No persistent in-memory state is assumed between lambda invocations.
