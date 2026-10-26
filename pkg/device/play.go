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

	// Resolve target TV
	targetDeviceName := "Default TV"
	deviceIP := input.DeviceIP
	webhookURL := input.WebhookURL

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
			} else if dev.IP != deviceIP {
				dev.IP = deviceIP
				_ = reg.Register(ctx, *dev)
			}
			if webhookURL == "" {
				webhookURL = dev.WebhookURL
			}
		} else if input.Device != "" {
			targetDeviceName = input.Device
		}
	} else if input.Device != "" {
		targetDeviceName = input.Device
	}

	if webhookURL == "" {
		webhookURL = os.Getenv("TV_WEBHOOK_URL")
	}

	// Build ADB command (targeted with IP if known)
	adbCommand := fmt.Sprintf(`adb shell am start -a android.intent.action.VIEW -d "%s"`, deepLink)
	if deviceIP != "" {
		adbCommand = fmt.Sprintf(`adb connect %s:5555 && adb -s %s:5555 shell am start -a android.intent.action.VIEW -d "%s"`, deviceIP, deviceIP, deepLink)
	}

	webhookSent := false
	if webhookURL != "" {
		payload := map[string]any{
			"media_type":    mediaType,
			"imdb_id":       input.IMDbID,
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

	titleDisplay := input.IMDbID
	if input.Title != "" {
		titleDisplay = fmt.Sprintf("%q (%s)", input.Title, input.IMDbID)
	}

	msg := fmt.Sprintf("Targeting [%s]: playback intent generated for %s.", targetDeviceName, titleDisplay)
	if deviceIP != "" {
		msg += fmt.Sprintf(" IP: %s.", deviceIP)
	}
	if webhookSent {
		msg += " Webhook successfully dispatched to your TV/Home Assistant."
	}

	return &PlayOnTVResult{
		IMDbID:       input.IMDbID,
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
