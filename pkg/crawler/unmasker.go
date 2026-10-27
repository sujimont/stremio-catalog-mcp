package crawler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/httputil"
)

var (
	iframeRegex  = regexp.MustCompile(`(?i)<iframe[^>]+src=["']([^"']+)["']`)
	videoSrcRegex = regexp.MustCompile(`(?i)<(?:video|source)[^>]+src=["']([^"']+)["']`)
	m3u8Regex    = regexp.MustCompile(`https?://[^\s"'<>]+\.m3u8[^\s"'<>]*`)
	mp4Regex     = regexp.MustCompile(`https?://[^\s"'<>]+\.mp4[^\s"'<>]*`)
	packedRegex  = regexp.MustCompile(`eval\(function\(p,a,c,k,e,?[dr]?\)\{.+?\}\s*\(\s*'(.+?)'\s*,\s*(\d+)\s*,\s*(\d+)\s*,\s*'([^']+)'\.split\('\|'\)`)
)

type UnmaskResult struct {
	OriginalURL   string `json:"original_url"`
	UnmaskedURL   string `json:"unmasked_url"`
	StreamType    string `json:"stream_type"` // hls (m3u8) or mp4
	Quality       string `json:"quality,omitempty"`
	RefererHeader string `json:"referer_header,omitempty"`
}

// UnpackJS unpacks Dean Edwards packed JavaScript code often used on streaming sites.
func UnpackJS(packed string) string {
	match := packedRegex.FindStringSubmatch(packed)
	if len(match) < 5 {
		return packed
	}
	payload := match[1]
	radix, err := strconv.Atoi(match[2])
	if err != nil || radix <= 0 {
		return packed
	}
	words := strings.Split(match[4], "|")

	wordRegex := regexp.MustCompile(`\b\w+\b`)
	unpacked := wordRegex.ReplaceAllStringFunc(payload, func(token string) string {
		val, err := strconv.ParseInt(token, radix, 64)
		if err != nil || int(val) >= len(words) {
			return token
		}
		replacement := words[val]
		if replacement == "" {
			return token
		}
		return replacement
	})

	return unpacked
}

// UnmaskStream resolves a masked streaming webpage or embed link to a raw playable video URL.
func UnmaskStream(ctx context.Context, client *http.Client, pageURL string) (*UnmaskResult, error) {
	if err := httputil.ValidatePublicURL(pageURL); err != nil {
		return nil, fmt.Errorf("SSRF protection: %w", err)
	}

	cleanP := strings.ToLower(pageURL)
	if strings.Contains(cleanP, ".m3u8") {
		return &UnmaskResult{OriginalURL: pageURL, UnmaskedURL: pageURL, StreamType: "hls"}, nil
	}
	if strings.Contains(cleanP, ".mp4") {
		return &UnmaskResult{OriginalURL: pageURL, UnmaskedURL: pageURL, StreamType: "mp4"}, nil
	}

	if client == nil {
		client = httputil.NewSafeClient(15 * time.Second)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Referer", pageURL)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch stream page: %w", err)
	}
	defer resp.Body.Close()

	finalURL := resp.Request.URL.String()
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "mpegurl") || strings.Contains(contentType, "hls") {
		return &UnmaskResult{OriginalURL: pageURL, UnmaskedURL: finalURL, StreamType: "hls"}, nil
	}
	if strings.Contains(contentType, "video/") || strings.Contains(contentType, "mp4") {
		return &UnmaskResult{OriginalURL: pageURL, UnmaskedURL: finalURL, StreamType: "mp4"}, nil
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	body := string(bodyBytes)

	// Step 1: Check if the response body itself contains packed JS, unpack it
	if strings.Contains(body, "eval(function(p,a,c,k,e") {
		body = body + "\n" + UnpackJS(body)
	}

	// Step 2: Search for direct m3u8 HLS streams
	if match := m3u8Regex.FindString(body); match != "" {
		return &UnmaskResult{
			OriginalURL:   pageURL,
			UnmaskedURL:   cleanURL(match),
			StreamType:    "hls",
			RefererHeader: finalURL,
		}, nil
	}

	// Step 3: Search for direct MP4 stream
	if match := mp4Regex.FindString(body); match != "" {
		return &UnmaskResult{
			OriginalURL:   pageURL,
			UnmaskedURL:   cleanURL(match),
			StreamType:    "mp4",
			RefererHeader: finalURL,
		}, nil
	}

	// Step 4: Search for video/source elements
	if match := videoSrcRegex.FindStringSubmatch(body); len(match) > 1 {
		raw := match[1]
		resolved := resolveRelativeURL(finalURL, raw)
		stype := "mp4"
		if strings.Contains(resolved, ".m3u8") {
			stype = "hls"
		}
		return &UnmaskResult{
			OriginalURL:   pageURL,
			UnmaskedURL:   resolved,
			StreamType:    stype,
			RefererHeader: finalURL,
		}, nil
	}

	// Step 5: Check for embedded iframe and recursively inspect one level
	if match := iframeRegex.FindStringSubmatch(body); len(match) > 1 {
		iframeURL := resolveRelativeURL(finalURL, match[1])
		if !strings.EqualFold(iframeURL, pageURL) && !strings.EqualFold(iframeURL, finalURL) {
			subResult, err := UnmaskStream(ctx, client, iframeURL)
			if err == nil && subResult != nil {
				subResult.OriginalURL = pageURL
				return subResult, nil
			}
		}
	}

	return nil, fmt.Errorf("could not unmask playable stream from %s", pageURL)
}

func cleanURL(u string) string {
	u = strings.Trim(u, `"'\,;`)
	u = strings.ReplaceAll(u, `\/`, `/`)
	return u
}

func resolveRelativeURL(base, target string) string {
	target = cleanURL(target)
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return target
	}
	if strings.HasPrefix(target, "//") {
		return "https:" + target
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return target
	}
	targetURL, err := url.Parse(target)
	if err != nil {
		return target
	}
	return baseURL.ResolveReference(targetURL).String()
}
