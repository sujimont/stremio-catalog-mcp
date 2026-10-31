# Stremio AI Agent MCP

**Autonomous Stremio & Debrid AI Agent for ChatGPT, Claude, and MCP Clients.**

This high-performance [Model Context Protocol](https://modelcontextprotocol.io/) server connects your AI assistant directly to your Stremio ecosystem: managing custom catalog rows, organizing Home layout, discovering regional cinema, checking stream health via Torrentio, crawling torrent forums (e.g. 1TamilMV), auto-ingesting magnets to TorBox, and managing installed addons.

---

## Capabilities & Tool Surface

The server exposes 15 purpose-built MCP tools across 7 operational domains:

### 1. Catalog & Row Content
* **`catalogs_get`**: Reads your custom catalog rows, ordered titles, and saved poster URLs. Supports multi-profile isolation.
* **`catalogs_update`**: Batched mutations (create, edit, delete, reorder titles, or prune watched titles) with optimistic Redis compare-and-swap (CAS) concurrency protection.
* **`torbox_catalog_sync`**: Automatically cleans release names, resolves matching IMDb titles via Cinemeta, and syncs completed TorBox cloud uploads into a Stremio custom catalog row.

### 2. Home Screen Layout
* **`home_layout_get`**: Lists all installed addons and their catalog rows with Home screen visibility and order.
* **`home_layout_update`**: Batch show/hide catalog rows and reorder addons on the Stremio Home screen. Supports `dry_run` preview.

### 3. Library & Watch History
* **`watch_history_get`**: Reads watch history from your Stremio library with watch progress and completion state, enabling spoiler-safe recommendations.

### 4. Stream Pre-Checking & Verification
* **`stream_availability_check`**: Pre-verifies active torrent stream counts, highest resolution (4K/1080p), seeders, and instant TorBox cloud cache status for any movie or series episode (`tt...:season:episode`).

### 5. Deep Discovery & Regional Cinema
* **`tmdb_discover`**: Discovers cinema by regional language (Tamil, Malayalam, Hindi, Telugu, Korean, etc.), streaming provider (Netflix, Prime, Hotstar), genre, year, and rating.
* **`tmdb_similar`**: Finds similar and recommended movies/shows for any title or IMDb ID.

### 6. Autonomous Crawling & TorBox Cloud
* **`torbox_uploads_get`**: Queries active and completed torrents and web downloads from your TorBox account, detailing size, status, progress, and file breakdown.
* **`media_crawl_and_ingest`**: Autonomous media crawler. Accepts direct `magnet:?` links or crawls torrent forum topic URLs (e.g. 1TamilMV), verifies TorBox cache, enforces a 10 GB size safeguard, and automatically ingests the release to TorBox.
* **`stream_unmask`**: Resolves masked embed players into direct video URLs with optional TorBox WebDL ingestion.
* **Live "TorBox Cloud" Catalogue**: When `TORBOX_API_KEY` is configured, the addon automatically publishes a real-time `TorBox Cloud` catalog (`type: other`) with direct video streaming permalinks for all your uploads.

### 7. Addon & Household Device Management
* **`addons_manage`**: Chat-driven addon management: list installed addons, install from manifest URL, or safely uninstall addons.
* **`play_on_tv`**: Generates Stremio deep links and triggers Home Assistant/Android TV webhooks.
* **`devices_manage`**: Registers, lists, removes, or resets household smart TVs with local IP and webhook targets. Seeds initial configuration from `TV_DEVICES_JSON` so environment entries are preserved across registrations.

---

## Security & Architecture

* **SSRF Protection**: All outbound requests (forum crawling, video unmasking, model-supplied webhooks) go through a hardened `SafeHTTPClient` that blocks loopback, private RFC1918, cloud metadata (`169.254.169.254`), carrier-grade NAT (`100.64.0.0/10`), and multicast IP addresses across all DNS lookups and redirects. Webhooks supplied by the model or registered via `devices_manage` additionally require HTTPS. Only `TV_WEBHOOK_URL` (set by the operator in Vercel environment variables) is trusted without these restrictions, making it the correct place to configure LAN or Home Assistant addresses.
* **Command Injection Immunity**: All media IDs are validated with strict regex (`^tt\d+(:\d+:\d+)?$`), media types against enums, and IPs with `net.ParseIP`. Device IPs are validated both at registration time and again at playback time.
* **Atomic Device Registry**: Device registrations use Redis compare-and-swap (EVAL Lua script) to prevent concurrent overwrites.
* **Zero Secret Leakage**: API keys (`TORBOX_API_KEY`, `TMDB_API_KEY`, `STREMIO_AUTH_KEY`, `MCP_API_KEY`) live exclusively in server environment variables and are never accepted or emitted in chat context.
* **Honest Tool Annotations**: Discovery queries carry `readOnlyHint: true`, while mutating operations advertise `readOnlyHint: false` (and `destructiveHint: true` where items can be removed).
* **Flexible Authentication**: Supports standard `Authorization: Bearer <key>` header as well as `?key=<key>` URL query parameter.

---

## Deployment & Setup

Deploy as a stateless Go serverless function on Vercel:

1. Clone or fork this repository:
   ```bash
   git clone https://github.com/sujimont/stremio-catalog-mcp.git
   ```
2. Configure environment variables in Vercel (see `.env.example`):
   * `MCP_API_KEY`: Secret key for your MCP connection.
   * `UPSTASH_REDIS_REST_URL`: Upstash Redis REST endpoint.
   * `UPSTASH_REDIS_REST_TOKEN`: Upstash Redis REST token.
   * `STREMIO_AUTH_KEY`: (Optional) Your Stremio account auth key.
   * `TORBOX_API_KEY`: (Optional) TorBox API key for debrid caching and autonomous ingestion.
   * `TMDB_API_KEY`: (Optional) TMDB API key for regional discovery.
   * `TV_WEBHOOK_URL`: (Optional) Operator-trusted Home Assistant or Android TV webhook URL. **This is the only way to use a LAN address** (e.g. `http://homeassistant.local:8123/...`). Webhooks registered at runtime via the `devices_manage` tool must be public HTTPS URLs.
3. Add the MCP endpoint to ChatGPT:
   * **Custom MCP Server URL**: `https://your-deployment.vercel.app/mcp?key=YOUR_MCP_API_KEY`
   * Or use `Authorization: Bearer YOUR_MCP_API_KEY`.

---

## License

Open source under [Apache-2.0](LICENSE).
