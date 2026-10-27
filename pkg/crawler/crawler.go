package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/torbox"
)

type CrawlOptions struct {
	Title      string  `json:"title,omitempty"`
	IMDbID     string  `json:"imdb_id,omitempty"`
	MediaType  string  `json:"media_type,omitempty"`
	Season     int     `json:"season,omitempty"`
	Episode    int     `json:"episode,omitempty"`
	Magnet     string  `json:"magnet,omitempty"`
	PageURL    string  `json:"page_url,omitempty"`
	TorboxKey  string  `json:"torbox_key,omitempty"`
	MaxSizeGB  float64 `json:"max_size_gb,omitempty"`
	AutoIngest bool    `json:"auto_ingest,omitempty"`
	StreamBase string  `json:"stream_base,omitempty"`
}

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
	magnetRegex   = regexp.MustCompile(`(?i)magnet:\?[^\s"'<>]+`)
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

func extractMagnetInfo(rawMagnet string) (hash, title, quality string, sizeGB float64) {
	rawMagnet = html.UnescapeString(rawMagnet)
	hash = hex40Regex.FindString(rawMagnet)
	title = ""
	quality = "SD"
	sizeGB = 0

	if idx := strings.Index(rawMagnet, "dn="); idx != -1 {
		sub := rawMagnet[idx+3:]
		if amp := strings.Index(sub, "&"); amp != -1 {
			sub = sub[:amp]
		}
		if unescaped, err := url.QueryUnescape(sub); err == nil && unescaped != "" {
			title = strings.TrimSpace(unescaped)
		}
	}
	if title == "" && hash != "" {
		title = "Torrent " + hash[:8]
	}

	quality = detectQuality(title)
	sizeGB = parseFileSizeGB(title)

	if sizeGB == 0 {
		if xlIdx := strings.Index(rawMagnet, "xl="); xlIdx != -1 {
			sub := rawMagnet[xlIdx+3:]
			if amp := strings.Index(sub, "&"); amp != -1 {
				sub = sub[:amp]
			}
			if bytesVal, err := strconv.ParseInt(sub, 10, 64); err == nil && bytesVal > 0 {
				sizeGB = float64(bytesVal) / (1024 * 1024 * 1024)
			}
		}
	}

	return hash, title, quality, sizeGB
}

// CrawlAndIngest executes the autonomous media crawler & TorBox ingestion engine:
// 1. Direct Magnet: if opts.Magnet is provided, validates hash, checks TorBox cache, and ingests immediately.
// 2. Web / Forum Crawler: if opts.PageURL is provided (e.g. 1TamilMV topic), fetches the page, extracts all magnets, checks TorBox cache, and ingests the best release under opts.MaxSizeGB.
// 3. Torrent Search: searches torrent streams via Torrentio (supports movie, series with season/episode).
// 4. Quota protection: strictly filters out files exceeding opts.MaxSizeGB (default 10.0 GB).
func CrawlAndIngest(ctx context.Context, opts CrawlOptions) (*CrawlResult, error) {
	if opts.MediaType == "" {
		opts.MediaType = "movie"
	}
	if opts.MaxSizeGB <= 0 {
		opts.MaxSizeGB = 10.0 // Default 10GB limit per movie/episode
	}
	httpClient := &http.Client{Timeout: 15 * time.Second}

	var torboxClient *torbox.Client
	if opts.TorboxKey != "" {
		torboxClient = torbox.NewClient(opts.TorboxKey)
	}

	// Pathway 1: Direct Magnet Link
	if opts.Magnet != "" {
		magnet := html.UnescapeString(strings.TrimSpace(opts.Magnet))
		hash, cleanTitle, quality, size := extractMagnetInfo(magnet)
		if hash == "" {
			return nil, fmt.Errorf("invalid magnet link: no 40-character infohash found")
		}
		if opts.MaxSizeGB > 0 && size > opts.MaxSizeGB {
			return &CrawlResult{
				Query:      opts.Title,
				Title:      cleanTitle,
				Quality:    quality,
				FileSize:   formatFileSize(size),
				FileSizeGB: size,
				MagnetURI:  magnet,
				Message:    fmt.Sprintf("Magnet size (%s) exceeds your %.1f GB limit. Ingestion blocked to preserve TorBox storage.", formatFileSize(size), opts.MaxSizeGB),
			}, nil
		}

		isCached := false
		if torboxClient != nil {
			if cachedMap, err := torboxClient.CheckCached(ctx, []string{hash}); err == nil && cachedMap != nil {
				isCached = cachedMap[strings.ToLower(hash)]
			}
		}

		res := &CrawlResult{
			Query:        opts.Title,
			IMDbID:       opts.IMDbID,
			Source:       "magnet",
			Title:        cleanTitle,
			Quality:      quality,
			FileSize:     formatFileSize(size),
			FileSizeGB:   size,
			MagnetURI:    magnet,
			TorboxCached: isCached,
		}

		if opts.AutoIngest && torboxClient != nil {
			createResp, err := torboxClient.AddTorrent(ctx, magnet)
			if err != nil {
				return nil, fmt.Errorf("failed to add magnet to TorBox: %w", err)
			}
			res.TorboxIngested = true
			res.TorboxID = createResp.Data.TorrentID
			cacheMsg := ""
			if isCached {
				cacheMsg = " (Instantly cached on TorBox & ready to stream!)"
			}
			res.Message = fmt.Sprintf("Successfully added magnet to TorBox (Torrent ID: %d)%s. Quality: %s.", res.TorboxID, cacheMsg, quality)
			return res, nil
		}

		res.Message = fmt.Sprintf("Verified magnet link (%s). Cached on TorBox: %v.", quality, isCached)
		return res, nil
	}

	// Pathway 2: Torrent Forum / Webpage Link Crawler (1TamilMV, etc.)
	if opts.PageURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.PageURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch torrent page: %w", err)
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, err
		}
		body := string(bodyBytes)

		matches := magnetRegex.FindAllString(body, -1)
		if len(matches) == 0 {
			return &CrawlResult{
				Query:   opts.Title,
				Source:  "web_crawl",
				Message: fmt.Sprintf("No magnet links found on webpage %s.", opts.PageURL),
			}, nil
		}

		type magnetCandidate struct {
			raw     string
			hash    string
			title   string
			quality string
			sizeGB  float64
			cached  bool
		}

		var candidates []magnetCandidate
		var hashes []string
		for _, m := range matches {
			mClean := html.UnescapeString(m)
			h, t, q, s := extractMagnetInfo(mClean)
			if h != "" {
				candidates = append(candidates, magnetCandidate{
					raw:     mClean,
					hash:    h,
					title:   t,
					quality: q,
					sizeGB:  s,
				})
				hashes = append(hashes, h)
			}
		}

		cachedMap := make(map[string]bool)
		if torboxClient != nil && len(hashes) > 0 {
			if tbMap, _ := torboxClient.CheckCached(ctx, hashes); tbMap != nil {
				cachedMap = tbMap
			}
		}

		var allowed []magnetCandidate
		smallestSize := 0.0
		for _, c := range candidates {
			c.cached = cachedMap[strings.ToLower(c.hash)]
			if c.sizeGB > 0 && (smallestSize == 0 || c.sizeGB < smallestSize) {
				smallestSize = c.sizeGB
			}
			if opts.MaxSizeGB > 0 && c.sizeGB > 0 && c.sizeGB > opts.MaxSizeGB {
				continue
			}
			allowed = append(allowed, c)
		}

		if len(allowed) == 0 {
			return &CrawlResult{
				Query:      opts.Title,
				Source:     "web_crawl",
				FileSizeGB: smallestSize,
				FileSize:   formatFileSize(smallestSize),
				Message:    fmt.Sprintf("Found %d magnets on page, but all exceed the %.1f GB limit (smallest is %s). Ingestion to TorBox blocked to preserve quota.", len(candidates), opts.MaxSizeGB, formatFileSize(smallestSize)),
			}, nil
		}

		best := allowed[0]
		foundBest := false
		for _, c := range allowed {
			if c.cached && (c.quality == "1080p" || c.quality == "4K") {
				best = c
				foundBest = true
				break
			}
		}
		if !foundBest {
			for _, c := range allowed {
				if c.cached {
					best = c
					foundBest = true
					break
				}
			}
		}
		if !foundBest {
			for _, c := range allowed {
				if c.quality == "1080p" || c.quality == "4K" {
					best = c
					break
				}
			}
		}

		res := &CrawlResult{
			Query:        opts.Title,
			IMDbID:       opts.IMDbID,
			Source:       "web_crawl",
			Title:        best.title,
			Quality:      best.quality,
			FileSize:     formatFileSize(best.sizeGB),
			FileSizeGB:   best.sizeGB,
			MagnetURI:    best.raw,
			TorboxCached: best.cached,
		}

		if opts.AutoIngest && torboxClient != nil {
			createResp, err := torboxClient.AddTorrent(ctx, best.raw)
			if err != nil {
				return nil, fmt.Errorf("failed to add crawled magnet to TorBox: %w", err)
			}
			res.TorboxIngested = true
			res.TorboxID = createResp.Data.TorrentID
			cacheMsg := ""
			if best.cached {
				cacheMsg = " (Instantly cached on TorBox & ready to stream!)"
			}
			res.Message = fmt.Sprintf("Successfully crawled page and added best magnet to TorBox (Torrent ID: %d)%s. Title: %q (%s, %s).", res.TorboxID, cacheMsg, best.title, best.quality, res.FileSize)
			return res, nil
		}

		res.Message = fmt.Sprintf("Found %d magnets on page. Best match: %q (%s, %s). Cached on TorBox: %v.", len(candidates), best.title, best.quality, res.FileSize, best.cached)
		return res, nil
	}

	// Pathway 3: Search Torrents via Torrentio
	title := opts.Title
	imdbID := opts.IMDbID
	if imdbID == "" && title != "" {
		resolvedID, err := resolveIMDbFromCinemeta(ctx, httpClient, opts.MediaType, title)
		if err == nil && resolvedID != "" {
			imdbID = resolvedID
		}
	}

	if imdbID != "" {
		streamID := imdbID
		if opts.MediaType == "series" && !strings.Contains(streamID, ":") {
			s := opts.Season
			if s <= 0 {
				s = 1
			}
			ep := opts.Episode
			if ep <= 0 {
				ep = 1
			}
			streamID = fmt.Sprintf("%s:%d:%d", streamID, s, ep)
		}

		torrentURL := fmt.Sprintf("https://torrentio.strem.fun/stream/%s/%s.json", opts.MediaType, streamID)
		if opts.StreamBase != "" {
			torrentURL = fmt.Sprintf("%s/stream/%s/%s.json", strings.TrimRight(opts.StreamBase, "/"), opts.MediaType, streamID)
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

					bestStream, bestSize, smallestSize := pickBestStream(data.Streams, cachedMap, opts.MaxSizeGB, title)
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
							sizeText = fmt.Sprintf(" Size: %s (limit: %.1f GB).", sizeFormatted, opts.MaxSizeGB)
						}

						result := &CrawlResult{
							Query:        title,
							IMDbID:       streamID,
							Source:       "torrent",
							Title:        cleanTitle,
							Quality:      quality,
							FileSize:     sizeFormatted,
							FileSizeGB:   bestSize,
							MagnetURI:    magnet,
							StreamURL:    bestStream.URL,
							TorboxCached: isCached,
						}

						if opts.AutoIngest && torboxClient != nil {
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
					} else if smallestSize > opts.MaxSizeGB {
						return &CrawlResult{
							Query:      title,
							IMDbID:     streamID,
							Source:     "torrent",
							Title:      title,
							FileSize:   formatFileSize(smallestSize),
							FileSizeGB: smallestSize,
							Message:    fmt.Sprintf("Found %d torrents, but all exceed the %.1f GB limit (smallest is %s). Ingestion to TorBox blocked to protect storage. Pass max_size_gb to override.", len(data.Streams), opts.MaxSizeGB, formatFileSize(smallestSize)),
						}, nil
					}
				}
			}
		}
	}

	return &CrawlResult{
		Query:   title,
		IMDbID:  imdbID,
		Source:  "none",
		Message: fmt.Sprintf("No active torrents found for %q. Provide a direct magnet or torrent forum link (e.g. 1TamilMV topic page) to crawl and ingest to TorBox.", title),
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
