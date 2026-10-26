package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/torbox"
)

type CrawlResult struct {
	Query          string  `json:"query"`
	IMDbID         string  `json:"imdb_id,omitempty"`
	Source         string  `json:"source"` // "torrent" or "http_stream"
	Title          string  `json:"title"`
	Quality        string  `json:"quality,omitempty"`
	FileSize       string  `json:"file_size,omitempty"`
	FileSizeGB     float64 `json:"file_size_gb,omitempty"`
	Seeders        int     `json:"seeders,omitempty"`
	MagnetURI      string  `json:"magnet_uri,omitempty"`
	StreamURL      string  `json:"stream_url,omitempty"`
	TorboxCached   bool    `json:"torbox_cached,omitempty"`
	TorboxIngested bool    `json:"torbox_ingested,omitempty"`
	TorboxID       int     `json:"torbox_id,omitempty"`
	Message        string  `json:"message"`
}

type torrentioStream struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	InfoHash string `json:"infoHash"`
	URL      string `json:"url"`
}

type torrentioResponse struct {
	Streams []torrentioStream `json:"streams"`
}

var (
	hex40Regex    = regexp.MustCompile(`(?i)\b([0-9a-f]{40})\b`)
	diskSizeRegex = regexp.MustCompile(`(?i)(?:💾\s*|size:\s*)([0-9]+(?:\.[0-9]+)?)\s*(TB|TiB|GB|GiB|MB|MiB)\b`)
	anySizeRegex  = regexp.MustCompile(`(?i)\b([0-9]+(?:\.[0-9]+)?)\s*(TB|TiB|GB|GiB|MB|MiB)\b`)
)

func parseFileSizeGB(title string) float64 {
	match := diskSizeRegex.FindStringSubmatch(title)
	if len(match) < 3 {
		match = anySizeRegex.FindStringSubmatch(title)
	}
	if len(match) < 3 {
		return 0
	}
	val, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToUpper(match[2])
	switch {
	case strings.HasPrefix(unit, "TB") || strings.HasPrefix(unit, "TI"):
		return val * 1024.0
	case strings.HasPrefix(unit, "GB") || strings.HasPrefix(unit, "GI"):
		return val
	case strings.HasPrefix(unit, "MB") || strings.HasPrefix(unit, "MI"):
		return val / 1024.0
	default:
		return val
	}
}

func formatFileSize(sizeGB float64) string {
	if sizeGB <= 0 {
		return ""
	}
	if sizeGB < 1.0 {
		return fmt.Sprintf("%.0f MB", sizeGB*1024.0)
	}
	return fmt.Sprintf("%.2f GB", sizeGB)
}

func extractHash(s torrentioStream) string {
	if s.InfoHash != "" {
		return s.InfoHash
	}
	if s.URL != "" {
		if match := hex40Regex.FindString(s.URL); match != "" {
			return match
		}
	}
	return ""
}

// CrawlAndIngest executes the autonomous 2-tier search:
// 1. Searches torrent streams via Torrentio and checks TorBox cache.
// 2. Filters out massive files exceeding maxSizeGB (defaults to 10.0 GB).
// 3. If healthy torrents exist, optionally ingests the best magnet to TorBox.
// 4. If torrents are unavailable, crawls for unmasked HTTP streams.
func CrawlAndIngest(ctx context.Context, title, imdbID, mediaType, torboxKey string, maxSizeGB float64, autoIngest bool, streamBase string) (*CrawlResult, error) {
	if mediaType == "" {
		mediaType = "movie"
	}
	if maxSizeGB <= 0 {
		maxSizeGB = 10.0 // Default 10GB limit per movie/episode
	}
	httpClient := &http.Client{Timeout: 12 * time.Second}

	// Step 1: If IMDb ID is missing, try to resolve via Cinemeta search
	if imdbID == "" && title != "" {
		resolvedID, err := resolveIMDbFromCinemeta(ctx, httpClient, mediaType, title)
		if err == nil && resolvedID != "" {
			imdbID = resolvedID
		}
	}

	var torboxClient *torbox.Client
	if torboxKey != "" {
		torboxClient = torbox.NewClient(torboxKey)
	}

	// Tier 1: Search Torrents
	if imdbID != "" {
		torrentURL := fmt.Sprintf("https://torrentio.strem.fun/stream/%s/%s.json", mediaType, imdbID)
		if streamBase != "" {
			torrentURL = fmt.Sprintf("%s/stream/%s/%s.json", strings.TrimRight(streamBase, "/"), mediaType, imdbID)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, torrentURL, nil)
		if err == nil {
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
			req.Header.Set("Accept", "application/json, text/plain, */*")
			resp, err := httpClient.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				var data torrentioResponse
				_ = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&data)
				_ = resp.Body.Close()

				if len(data.Streams) > 0 {
					var hashes []string
					cachedMap := make(map[string]bool)
					for i := range data.Streams {
						s := &data.Streams[i]
						h := extractHash(*s)
						if h != "" {
							s.InfoHash = h
							if strings.Contains(s.Name, "[TB+]") {
								cachedMap[strings.ToLower(h)] = true
							}
							hashes = append(hashes, h)
						}
					}

					if torboxClient != nil && len(hashes) > 0 && len(cachedMap) == 0 {
						if tbMap, _ := torboxClient.CheckCached(ctx, hashes); tbMap != nil {
							cachedMap = tbMap
						}
					}

					// Find the best stream: filtered by max size (default 10GB), title match, cached first, then 4k/1080p, standalone
					bestStream, bestSize, smallestSize := pickBestStream(data.Streams, cachedMap, maxSizeGB, title)
					bestHash := extractHash(bestStream)
					if bestHash != "" {
						bestStream.InfoHash = bestHash
						cleanTitle := extractStreamName(bestStream.Title)
						if cleanTitle == "" {
							cleanTitle = title
						}

						trackers := []string{
							"udp://tracker.opentrackr.org:1337/announce",
							"udp://open.stealth.si:80/announce",
							"udp://tracker.torrent.eu.org:451/announce",
							"udp://explodie.org:6969/announce",
						}
						var trParts []string
						for _, tr := range trackers {
							trParts = append(trParts, "&tr="+url.QueryEscape(tr))
						}
						magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s%s", bestStream.InfoHash, url.QueryEscape(cleanTitle), strings.Join(trParts, ""))

						isCached := cachedMap != nil && cachedMap[strings.ToLower(bestStream.InfoHash)]
						quality := detectQuality(bestStream.Name + " " + bestStream.Title)
						sizeFormatted := formatFileSize(bestSize)

						sizeText := ""
						if sizeFormatted != "" {
							sizeText = fmt.Sprintf(" Size: %s (limit: %.1f GB).", sizeFormatted, maxSizeGB)
						}

						result := &CrawlResult{
							Query:        title,
							IMDbID:       imdbID,
							Source:       "torrent",
							Title:        cleanTitle,
							Quality:      quality,
							FileSize:     sizeFormatted,
							FileSizeGB:   bestSize,
							MagnetURI:    magnet,
							StreamURL:    bestStream.URL,
							TorboxCached: isCached,
						}

						if autoIngest && torboxClient != nil {
							createResp, err := torboxClient.AddTorrent(ctx, magnet)
							if err == nil && createResp != nil {
								result.TorboxIngested = true
								result.TorboxID = createResp.Data.TorrentID
								cacheMsg := ""
								if isCached {
									cacheMsg = " (Instantly cached & ready to stream!)"
								}
								result.Message = fmt.Sprintf("Successfully added to TorBox (Torrent ID: %d)%s. Quality: %s.%s", result.TorboxID, cacheMsg, quality, sizeText)
								return result, nil
							}
						}

						result.Message = fmt.Sprintf("Found high quality torrent (%s).%s Cached on TorBox: %v.", quality, sizeText, isCached)
						return result, nil
					} else if smallestSize > maxSizeGB {
						return &CrawlResult{
							Query:      title,
							IMDbID:     imdbID,
							Source:     "torrent",
							Title:      title,
							FileSize:   formatFileSize(smallestSize),
							FileSizeGB: smallestSize,
							Message:    fmt.Sprintf("Found %d torrents, but all exceed the %.1f GB limit (smallest is %s). Ingestion to TorBox blocked to protect storage. Pass max_size_gb to override.", len(data.Streams), maxSizeGB, formatFileSize(smallestSize)),
						}, nil
					}
				}
			}
		}
	}

	// Tier 2: HTTP Stream fallback / Crawl
	return &CrawlResult{
		Query:   title,
		IMDbID:  imdbID,
		Source:  "none",
		Message: fmt.Sprintf("No active torrents found for %q. Use unmask_stream with a direct streaming page URL to extract and ingest playable video.", title),
	}, nil
}

func resolveIMDbFromCinemeta(ctx context.Context, client *http.Client, mediaType, query string) (string, error) {
	u := fmt.Sprintf("https://v3-cinemeta.strem.io/catalog/%s/top/search=%s.json", mediaType, url.PathEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var data struct {
		Metas []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"metas"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if len(data.Metas) > 0 && strings.HasPrefix(data.Metas[0].ID, "tt") {
		return data.Metas[0].ID, nil
	}
	return "", fmt.Errorf("not found in Cinemeta")
}

func isPack(s torrentioStream) bool {
	lower := strings.ToLower(s.Name + " " + s.Title)
	return strings.Contains(lower, "pack") || strings.Contains(lower, "collection") || strings.Contains(lower, "boxset") || strings.Contains(lower, "movies") || strings.Contains(lower, "1-4") || strings.Contains(lower, "1-5") || strings.Contains(lower, "complete")
}

func matchesTitle(s torrentioStream, title string) bool {
	if title == "" {
		return true
	}
	clean := strings.ToLower(strings.TrimSpace(title))
	words := strings.Fields(clean)
	if len(words) == 0 {
		return true
	}
	content := strings.ToLower(s.Name + " " + s.Title)
	return strings.Contains(content, words[0])
}

func pickBestStream(streams []torrentioStream, cachedMap map[string]bool, maxSizeGB float64, expectedTitle string) (torrentioStream, float64, float64) {
	if len(streams) == 0 {
		return torrentioStream{}, 0, 0
	}

	type streamInfo struct {
		stream  torrentioStream
		sizeGB  float64
		quality string
		cached  bool
		isPack  bool
		matched bool
	}

	var candidates []streamInfo
	smallestSize := 0.0

	for _, s := range streams {
		size := parseFileSizeGB(s.Title)
		matched := matchesTitle(s, expectedTitle)
		if matched && size > 0 && (smallestSize == 0 || size < smallestSize) {
			smallestSize = size
		}
		cached := cachedMap != nil && cachedMap[strings.ToLower(s.InfoHash)]
		candidates = append(candidates, streamInfo{
			stream:  s,
			sizeGB:  size,
			quality: detectQuality(s.Name + " " + s.Title),
			cached:  cached,
			isPack:  isPack(s),
			matched: matched,
		})
	}

	// Filter by maxSizeGB and title relevance:
	var allowed []streamInfo
	for _, c := range candidates {
		if !c.matched {
			continue
		}
		if maxSizeGB > 0 && c.sizeGB > 0 && c.sizeGB > maxSizeGB {
			continue
		}
		allowed = append(allowed, c)
	}

	if len(allowed) == 0 {
		return torrentioStream{}, 0, smallestSize
	}

	// 1. Check for cached 4K or 1080p standalone release under max size
	for _, c := range allowed {
		if c.cached && !c.isPack && (c.quality == "4K" || c.quality == "1080p") {
			return c.stream, c.sizeGB, smallestSize
		}
	}

	// 2. Any cached standalone stream under max size
	for _, c := range allowed {
		if c.cached && !c.isPack {
			return c.stream, c.sizeGB, smallestSize
		}
	}

	// 3. First 1080p or 4k standalone stream under max size
	for _, c := range allowed {
		if !c.isPack && (c.quality == "1080p" || c.quality == "4K") {
			return c.stream, c.sizeGB, smallestSize
		}
	}

	// 4. Any standalone stream under max size
	for _, c := range allowed {
		if !c.isPack {
			return c.stream, c.sizeGB, smallestSize
		}
	}

	// 5. Fallback to any allowed stream
	return allowed[0].stream, allowed[0].sizeGB, smallestSize
}

func detectQuality(text string) string {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "4k") || strings.Contains(lower, "2160p") {
		return "4K"
	}
	if strings.Contains(lower, "1080p") || strings.Contains(lower, "fhd") {
		return "1080p"
	}
	if strings.Contains(lower, "720p") || strings.Contains(lower, "hd") {
		return "720p"
	}
	return "SD"
}

func extractStreamName(title string) string {
	lines := strings.Split(title, "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return ""
}
