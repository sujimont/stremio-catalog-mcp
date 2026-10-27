package device

import (
	"context"
	"os"
	"testing"
)

func TestTriggerPlayValidation(t *testing.T) {
	ctx := context.Background()

	// Invalid IMDb ID
	_, err := TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID: "bad_id; rm -rf /",
	})
	if err == nil {
		t.Errorf("expected error for malformed IMDb ID, got nil")
	}

	// Invalid media_type
	_, err = TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:    "tt1375666",
		MediaType: "malicious_type",
	})
	if err == nil {
		t.Errorf("expected error for invalid media_type, got nil")
	}

	// Invalid IP with command injection
	_, err = TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:   "tt1375666",
		DeviceIP: "192.168.1.1; echo pwned",
	})
	if err == nil {
		t.Errorf("expected error for shell injection in device IP, got nil")
	}

	// Model-supplied ad-hoc webhook with SSRF target must fail validation
	_, err = TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:     "tt1375666",
		MediaType:  "movie",
		WebhookURL: "http://127.0.0.1:8123/api/webhook",
	})
	if err == nil {
		t.Errorf("expected SSRF error for model-supplied local webhook URL, got nil")
	}

	// Operator-configured webhook (TV_WEBHOOK_URL) is trusted and must not fail validation
	os.Setenv("TV_WEBHOOK_URL", "http://homeassistant.local:8123/api/webhook/test")
	defer os.Unsetenv("TV_WEBHOOK_URL")

	resOperator, err := TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:    "tt1375666",
		MediaType: "movie",
		Title:     "Inception",
	})
	if err != nil {
		t.Fatalf("operator webhook should not fail SSRF validation: %v", err)
	}
	if resOperator.DeepLink != "stremio://detail/movie/tt1375666" {
		t.Errorf("expected deep link stremio://detail/movie/tt1375666, got %s", resOperator.DeepLink)
	}

	// Valid movie play
	res, err := TriggerPlay(ctx, nil, PlayOnTVInput{
		IMDbID:    "tt1375666",
		MediaType: "movie",
		Title:     "Inception",
		DeviceIP:  "192.168.1.100",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.DeepLink != "stremio://detail/movie/tt1375666" {
		t.Errorf("expected deep link stremio://detail/movie/tt1375666, got %s", res.DeepLink)
	}
}
