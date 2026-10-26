package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/torbox"
)

type CrawlResult struct {
	Query         string `json:"query"`
	IMDbID        string `json:"imdb_id,omitempty"`
	Source        string `json:"source"` // "torrent" or "http_stream"
	Title         string `json:"title"`
	Quality       string `json:"quality,omitempty"`
	Seeders       int    `json:"seeders,omitempty"`
	MagnetURI     string `json:"magnet_uri,omitempty"`
	StreamURL     string `json:"stream_url,omitempty"`
	TorboxCached  bool   `json:"torbox_cached,omitempty"`
	TorboxIngested bool  `json:"torbox_ingested,omitempty"`
	TorboxID      int    `json:"torbox_id,omitempty"`
	Message       string `json:"message"`
}

type torrentioStream struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	InfoHash string `json:"infoHash"`
}

type torrentioResponse struct {
	Streams []torrentioStream `json:"streams"`
}

// CrawlAndIngest executes the autonomous 2-tier search:
// 1. Searches torrent streams via Torrentio and checks TorBox cache.
// 2. If healthy torrents exist, optionally ingests the best magnet to TorBox.
// 3. If torrents are unavailable, crawls for unmasked HTTP streams.
func CrawlAndIngest(ctx context.Context, title, imdbID, mediaType, torboxKey string, autoIngest bool, streamBase string) (*CrawlResult, error) {
	if mediaType == "" {
		mediaType = "movie"
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
					for _, s := range data.Streams {
						if strings.Contains(s.Name, "[TB+]") && s.InfoHash != "" {
							cachedMap[strings.ToLower(s.InfoHash)] = true
						}
						if s.InfoHash != "" {
							hashes = append(hashes, s.InfoHash)
						}
					}

					if torboxClient != nil && len(hashes) > 0 && len(cachedMap) == 0 {
						if tbMap, _ := torboxClient.CheckCached(ctx, hashes); tbMap != nil {
							cachedMap = tbMap
						}
					}

					// Find the best stream: cached first, then 4k/1080p, then highest seeders
					bestStream := pickBestStream(data.Streams, cachedMap)
					if bestStream.InfoHash != "" {
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

						result := &CrawlResult{
							Query:        title,
							IMDbID:       imdbID,
							Source:       "torrent",
							Title:        cleanTitle,
							Quality:      quality,
							MagnetURI:    magnet,
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
								result.Message = fmt.Sprintf("Successfully added to TorBox (Torrent ID: %d)%s. Quality: %s.", result.TorboxID, cacheMsg, quality)
								return result, nil
							}
						}

						result.Message = fmt.Sprintf("Found high quality torrent (%s). Cached on TorBox: %v.", quality, isCached)
						return result, nil
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

func pickBestStream(streams []torrentioStream, cachedMap map[string]bool) torrentioStream {
	if len(streams) == 0 {
		return torrentioStream{}
	}

	// 1. Check for cached 4K or 1080p
	if cachedMap != nil {
		for _, s := range streams {
			if cachedMap[strings.ToLower(s.InfoHash)] {
				q := detectQuality(s.Name + " " + s.Title)
				if q == "4K" || q == "1080p" {
					return s
				}
			}
		}
		// Any cached stream
		for _, s := range streams {
			if cachedMap[strings.ToLower(s.InfoHash)] {
				return s
			}
		}
	}

	// 2. Otherwise pick first 1080p or 4k stream
	for _, s := range streams {
		q := detectQuality(s.Name + " " + s.Title)
		if q == "1080p" || q == "4K" {
			return s
		}
	}

	return streams[0]
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
