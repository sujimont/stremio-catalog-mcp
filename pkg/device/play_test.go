package device

import (
	"context"
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
