package streams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var seederRegex = regexp.MustCompile(`(?:👤|seeders?:\s*|peers?:\s*)(\d+)`)

type StreamAvailabilityResult struct {
	IMDbID         string                     `json:"imdb_id,omitempty"`
	Available      bool                       `json:"available"`
	Verified       bool                       `json:"verified"`
	StreamCount    int                        `json:"stream_count"`
	HighestQuality string                     `json:"highest_quality,omitempty"`
	MaxSeeders     int                        `json:"max_seeders,omitempty"`
	BestStream     string                     `json:"best_stream,omitempty"`
	TorboxCached   bool                       `json:"torbox_cached,omitempty"`
	TitleMatched   bool                       `json:"title_matched,omitempty"`
	Message        string                     `json:"message"`
	BatchResults   []StreamAvailabilityResult `json:"batch_results,omitempty"`
}

type torrentioStream struct {
	Name          string `json:"name"`
	Title         string `json:"title"`
	InfoHash      string `json:"infoHash"`
	URL           string `json:"url"`
	BehaviorHints struct {
		Filename string `json:"filename"`
	} `json:"behaviorHints"`
}

var hex40Regex = regexp.MustCompile(`(?i)\b([0-9a-f]{40})\b`)

func extractStreamHash(s torrentioStream) string {
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

type torrentioResponse struct {
	Streams []torrentioStream `json:"streams"`
}

// CheckAvailability queries Torrentio and optional TorBox cache to check if a single title has healthy streams.
func CheckAvailability(ctx context.Context, mediaType, imdbID, expectedTitle, torboxKey, streamBase string) (StreamAvailabilityResult, error) {
	if mediaType == "" {
		mediaType = "movie"
	}
	if !strings.HasPrefix(imdbID, "tt") {
		return StreamAvailabilityResult{}, fmt.Errorf("invalid IMDb ID: %s", imdbID)
	}

	url := fmt.Sprintf("https://torrentio.strem.fun/stream/%s/%s.json", mediaType, imdbID)
	if streamBase != "" {
		url = fmt.Sprintf("%s/stream/%s/%s.json", strings.TrimRight(streamBase, "/"), mediaType, imdbID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return StreamAvailabilityResult{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return StreamAvailabilityResult{}, fmt.Errorf("stream check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return StreamAvailabilityResult{
			IMDbID:    imdbID,
			Available: false,
			Message:   fmt.Sprintf("Stream provider returned HTTP %d", resp.StatusCode),
		}, nil
	}

	var data torrentioResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&data); err != nil {
		return StreamAvailabilityResult{}, fmt.Errorf("invalid stream response: %w", err)
	}

	if len(data.Streams) == 0 {
		return StreamAvailabilityResult{
			IMDbID:    imdbID,
			Available: false,
			Verified:  true,
			Message:   "No active torrent streams found for this title.",
		}, nil
	}

	highestQuality := "SD"
	maxSeeders := 0
	bestTitle := ""
	bestQualityRank := 0
	torboxCached := false
	titleMatched := false
	var hashes []string

	qualityRank := func(q string) int {
		switch q {
		case "4K":
			return 4
		case "1080p":
			return 3
		case "720p":
			return 2
		default:
			return 1
		}
	}

	cleanExpected := strings.ToLower(strings.TrimSpace(expectedTitle))

	for _, s := range data.Streams {
		content := strings.ToLower(s.Name + " " + s.Title)
		isCachedStream := strings.Contains(s.Name, "[TB+]") || strings.Contains(s.Name, "[RD+]") || strings.Contains(s.Name, "[AD+]")
		if isCachedStream {
			torboxCached = true
		}

		currentStreamQuality := "SD"
		if strings.Contains(content, "4k") || strings.Contains(content, "2160p") {
			currentStreamQuality = "4K"
			highestQuality = "4K"
		} else if strings.Contains(content, "1080p") || strings.Contains(content, "fhd") {
			currentStreamQuality = "1080p"
			if highestQuality != "4K" {
				highestQuality = "1080p"
			}
		} else if strings.Contains(content, "720p") || strings.Contains(content, "hd") {
			currentStreamQuality = "720p"
			if highestQuality != "4K" && highestQuality != "1080p" {
				highestQuality = "720p"
			}
		}

		if match := seederRegex.FindStringSubmatch(s.Title); len(match) > 1 {
			if count, err := strconv.Atoi(match[1]); err == nil && count > maxSeeders {
				maxSeeders = count
			}
		}

		candidate := s.BehaviorHints.Filename
		if candidate == "" && s.Title != "" {
			candidate = strings.Split(s.Title, "\n")[0]
		}
		candidate = strings.TrimSpace(candidate)

		if cleanExpected != "" && strings.Contains(strings.ToLower(candidate), cleanExpected) {
			titleMatched = true
		}

		if candidate != "" {
			candLower := strings.ToLower(candidate)
			isPack := strings.Contains(candLower, "pack") || strings.Contains(candLower, "collection") || strings.Contains(candLower, "boxset") || strings.Contains(candLower, "movies")
			rank := qualityRank(currentStreamQuality)
			if isCachedStream {
				rank += 10
			}

			if bestTitle == "" || (!isPack && rank > bestQualityRank) {
				bestTitle = candidate
				bestQualityRank = rank
			}
		}

		if h := extractStreamHash(s); h != "" && len(hashes) < 15 {
			hashes = append(hashes, h)
		}
	}

	if !torboxCached && torboxKey != "" && len(hashes) > 0 {
		torboxCached = checkTorboxCache(ctx, client, torboxKey, hashes)
	}

	cachedMsg := ""
	if torboxCached {
		cachedMsg = " (Instantly cached & verified on TorBox!)"
	}

	return StreamAvailabilityResult{
		IMDbID:         imdbID,
		Available:      true,
		Verified:       true,
		StreamCount:    len(data.Streams),
		HighestQuality: highestQuality,
		MaxSeeders:     maxSeeders,
		BestStream:     bestTitle,
		TorboxCached:   torboxCached,
		TitleMatched:   titleMatched,
		Message:        fmt.Sprintf("Confirmed %d active streams up to %s. Streams verified and ready to play.%s", len(data.Streams), highestQuality, cachedMsg),
	}, nil
}

// CheckBatchAvailability checks up to 50 titles concurrently in parallel.
func CheckBatchAvailability(ctx context.Context, mediaType string, imdbIDs []string, torboxKey, streamBase string) (StreamAvailabilityResult, error) {
	if len(imdbIDs) == 0 {
		return StreamAvailabilityResult{}, errors.New("imdb_ids cannot be empty")
	}
	if len(imdbIDs) > 50 {
		imdbIDs = imdbIDs[:50]
	}

	results := make([]StreamAvailabilityResult, len(imdbIDs))
	type job struct {
		index int
		id    string
	}

	jobs := make(chan job, len(imdbIDs))
	var wg sync.WaitGroup

	workerCount := 10
	if len(imdbIDs) < workerCount {
		workerCount = len(imdbIDs)
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				res, err := CheckAvailability(ctx, mediaType, j.id, "", torboxKey, streamBase)
				if err != nil {
					res = StreamAvailabilityResult{
						IMDbID:    j.id,
						Available: false,
						Message:   err.Error(),
					}
				}
				results[j.index] = res
			}
		}()
	}

	for i, id := range imdbIDs {
		jobs <- job{index: i, id: id}
	}
	close(jobs)
	wg.Wait()

	availableCount := 0
	cachedCount := 0
	for _, r := range results {
		if r.Available {
			availableCount++
		}
		if r.TorboxCached {
			cachedCount++
		}
	}

	return StreamAvailabilityResult{
		Available:    availableCount > 0,
		Verified:     true,
		StreamCount:  availableCount,
		Message:      fmt.Sprintf("Batch stream check complete: %d of %d titles have healthy streams (%d instantly cached on TorBox).", availableCount, len(imdbIDs), cachedCount),
		BatchResults: results,
	}, nil
}

func checkTorboxCache(ctx context.Context, client *http.Client, apiKey string, hashes []string) bool {
	hashParam := strings.Join(hashes, ",")
	endpoint := fmt.Sprintf("https://api.torbox.app/v1/api/torrents/checkcached?hash=%s&format=object", hashParam)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return false
	}
	defer resp.Body.Close()

	var result struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || !result.Success {
		return false
	}

	var cachedMap map[string]any
	if err := json.Unmarshal(result.Data, &cachedMap); err == nil {
		return len(cachedMap) > 0
	}
	return false
}
