package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type PlayOnTVInput struct {
	MediaType  string `json:"media_type,omitempty" jsonschema:"movie or series (default movie)"`
	IMDbID     string `json:"imdb_id" jsonschema:"IMDb ID (e.g. tt1375666)"`
	Title      string `json:"title,omitempty" jsonschema:"Movie or show title"`
	Season     int    `json:"season,omitempty" jsonschema:"Season number for series"`
	Episode    int    `json:"episode,omitempty" jsonschema:"Episode number for series"`
	WebhookURL string `json:"webhook_url,omitempty" jsonschema:"Optional Home Assistant or TV webhook URL"`
}

type PlayOnTVResult struct {
	IMDbID      string `json:"imdb_id"`
	Title       string `json:"title,omitempty"`
	DeepLink    string `json:"deep_link"`
	WebLink     string `json:"web_link"`
	ADBCommand  string `json:"adb_command"`
	WebhookSent bool   `json:"webhook_sent"`
	Message     string `json:"message"`
}

// TriggerPlay builds Stremio deep links and optionally triggers Home Assistant / TV webhook.
func TriggerPlay(ctx context.Context, input PlayOnTVInput) (*PlayOnTVResult, error) {
	if !strings.HasPrefix(input.IMDbID, "tt") {
		return nil, fmt.Errorf("invalid IMDb ID: %s (must begin with tt)", input.IMDbID)
	}

	mediaType := input.MediaType
	if mediaType == "" {
		mediaType = "movie"
	}

	deepLink := fmt.Sprintf("stremio://detail/%s/%s", mediaType, input.IMDbID)
	webLink := fmt.Sprintf("https://web.stremio.com/#/detail/%s/%s", mediaType, input.IMDbID)

	if mediaType == "series" && input.Season > 0 && input.Episode > 0 {
		videoID := fmt.Sprintf("%s:%d:%d", input.IMDbID, input.Season, input.Episode)
		deepLink = fmt.Sprintf("stremio://detail/series/%s/%s", input.IMDbID, videoID)
		webLink = fmt.Sprintf("https://web.stremio.com/#/detail/series/%s/%s", input.IMDbID, videoID)
	}

	adbCommand := fmt.Sprintf(`adb shell am start -a android.intent.action.VIEW -d "%s"`, deepLink)

	webhookURL := input.WebhookURL
	if webhookURL == "" {
		webhookURL = os.Getenv("TV_WEBHOOK_URL")
	}

	webhookSent := false
	if webhookURL != "" {
		payload := map[string]any{
			"media_type":  mediaType,
			"imdb_id":     input.IMDbID,
			"title":       input.Title,
			"deep_link":   deepLink,
			"web_link":    webLink,
			"adb_command": adbCommand,
			"timestamp":   time.Now().Unix(),
		}
		data, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(data))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					webhookSent = true
				}
			}
		}
	}

	msg := fmt.Sprintf("Remote playback ready for %s.", input.IMDbID)
	if input.Title != "" {
		msg = fmt.Sprintf("Remote playback ready for %q (%s).", input.Title, input.IMDbID)
	}
	if webhookSent {
		msg += " Webhook successfully dispatched to your TV/Home Assistant."
	}

	return &PlayOnTVResult{
		IMDbID:      input.IMDbID,
		Title:       input.Title,
		DeepLink:    deepLink,
		WebLink:     webLink,
		ADBCommand:  adbCommand,
		WebhookSent: webhookSent,
		Message:     msg,
	}, nil
}
