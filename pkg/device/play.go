package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/httputil"
)

var (
	imdbIDRegex = regexp.MustCompile(`^tt\d+(:\d+:\d+)?$`)
)

type PlayOnTVInput struct {
	IMDbID     string `json:"imdb_id" jsonschema:"IMDb ID (e.g. tt1375666)"`
	MediaType  string `json:"media_type,omitempty" jsonschema:"movie or series (default movie)"`
	Title      string `json:"title,omitempty" jsonschema:"Movie or show title"`
	Season     int    `json:"season,omitempty" jsonschema:"Season number for series"`
	Episode    int    `json:"episode,omitempty" jsonschema:"Episode number for series"`
	Device     string `json:"device,omitempty" jsonschema:"Target TV room name or alias (e.g. living_room, bedroom, kids_room)"`
	DeviceIP   string `json:"device_ip,omitempty" jsonschema:"Optional direct TV IP address override (e.g. 192.168.1.50)"`
	WebhookURL string `json:"webhook_url,omitempty" jsonschema:"Optional Home Assistant or TV webhook URL override"`
}

type PlayOnTVResult struct {
	IMDbID       string `json:"imdb_id"`
	Title        string `json:"title,omitempty"`
	TargetDevice string `json:"target_device"`
	DeviceIP     string `json:"device_ip,omitempty"`
	DeepLink     string `json:"deep_link"`
	WebLink      string `json:"web_link"`
	ADBCommand   string `json:"adb_command"`
	WebhookSent  bool   `json:"webhook_sent"`
	Message      string `json:"message"`
}

// TriggerPlay builds Stremio deep links and targets a specific TV in the household.
func TriggerPlay(ctx context.Context, reg *Registry, input PlayOnTVInput) (*PlayOnTVResult, error) {
	cleanID := strings.TrimSpace(input.IMDbID)
	if !imdbIDRegex.MatchString(cleanID) {
		return nil, fmt.Errorf("invalid IMDb ID %q: must match ^tt\\d+(:\\d+:\\d+)?$", cleanID)
	}

	mediaType := strings.ToLower(strings.TrimSpace(input.MediaType))
	if mediaType == "" {
		mediaType = "movie"
	}
	if mediaType != "movie" && mediaType != "series" {
		return nil, fmt.Errorf("invalid media_type %q: must be 'movie' or 'series'", mediaType)
	}

	deepLink := fmt.Sprintf("stremio://detail/%s/%s", mediaType, cleanID)
	webLink := fmt.Sprintf("https://web.stremio.com/#/detail/%s/%s", mediaType, cleanID)

	if mediaType == "series" && input.Season > 0 && input.Episode > 0 {
		videoID := fmt.Sprintf("%s:%d:%d", cleanID, input.Season, input.Episode)
		deepLink = fmt.Sprintf("stremio://detail/series/%s/%s", cleanID, videoID)
		webLink = fmt.Sprintf("https://web.stremio.com/#/detail/series/%s/%s", cleanID, videoID)
	}

	// Resolve target TV
	targetDeviceName := "Default TV"
	deviceIP := strings.TrimSpace(input.DeviceIP)
	webhookURL := strings.TrimSpace(input.WebhookURL)
	isModelSuppliedWebhook := webhookURL != ""

	if deviceIP != "" {
		if ip := net.ParseIP(deviceIP); ip == nil {
			return nil, fmt.Errorf("invalid device IP address %q", deviceIP)
		}
		if strings.ContainsAny(deviceIP, ";|&`$<>\"'\n\r ") {
			return nil, fmt.Errorf("device IP contains invalid characters")
		}
	}

	if reg != nil {
		targetQuery := input.Device
		if targetQuery == "" {
			targetQuery = "living_room"
		}
		dev, err := reg.Find(ctx, targetQuery)
		if err == nil && dev != nil {
			targetDeviceName = dev.Name
			if deviceIP == "" {
				deviceIP = dev.IP
			}
			if webhookURL == "" && dev.WebhookURL != "" {
				webhookURL = dev.WebhookURL
				isModelSuppliedWebhook = false
			}
		} else if input.Device != "" {
			targetDeviceName = input.Device
		}
	} else if input.Device != "" {
		targetDeviceName = input.Device
	}

	if webhookURL == "" && os.Getenv("TV_WEBHOOK_URL") != "" {
		webhookURL = os.Getenv("TV_WEBHOOK_URL")
		isModelSuppliedWebhook = false
	}

	// Build safe ADB command
	adbCommand := fmt.Sprintf(`adb shell am start -a android.intent.action.VIEW -d "%s"`, deepLink)
	if deviceIP != "" {
		adbCommand = fmt.Sprintf(`adb connect %s:5555 && adb -s %s:5555 shell am start -a android.intent.action.VIEW -d "%s"`, deviceIP, deviceIP, deepLink)
	}

	webhookSent := false
	if webhookURL != "" {
		var client *http.Client
		if isModelSuppliedWebhook {
			// Ad-hoc model supplied webhook: validate against SSRF and dial via SafeClient
			if err := httputil.ValidatePublicURL(webhookURL); err != nil {
				return nil, fmt.Errorf("invalid webhook URL (SSRF protected): %w", err)
			}
			client = httputil.NewSafeClient(5 * time.Second)
		} else {
			// Operator-configured webhook (env var or device registry): trusted, allows LAN/Home Assistant
			client = &http.Client{Timeout: 5 * time.Second}
		}

		payload := map[string]any{
			"media_type":    mediaType,
			"imdb_id":       cleanID,
			"title":         input.Title,
			"target_device": targetDeviceName,
			"device_ip":     deviceIP,
			"deep_link":     deepLink,
			"web_link":      webLink,
			"adb_command":   adbCommand,
			"timestamp":     time.Now().Unix(),
		}
		data, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(data))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					webhookSent = true
				}
			}
		}
	}

	titleDisplay := cleanID
	if input.Title != "" {
		titleDisplay = fmt.Sprintf("%q (%s)", input.Title, cleanID)
	}

	msg := fmt.Sprintf("Targeting [%s]: playback intent generated for %s.", targetDeviceName, titleDisplay)
	if deviceIP != "" {
		msg += fmt.Sprintf(" Direct ADB command prepared for IP %s (execute on home network / Home Assistant bridge).", deviceIP)
	}
	if webhookSent {
		msg += " Webhook successfully dispatched to your TV/Home Assistant."
	}

	return &PlayOnTVResult{
		IMDbID:       cleanID,
		Title:        input.Title,
		TargetDevice: targetDeviceName,
		DeviceIP:     deviceIP,
		DeepLink:     deepLink,
		WebLink:      webLink,
		ADBCommand:   adbCommand,
		WebhookSent:  webhookSent,
		Message:      msg,
	}, nil
}
