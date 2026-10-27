package crawler

import (
	"strings"
	"testing"
)

func TestParseFileSizeGB(t *testing.T) {
	tests := []struct {
		title    string
		expected float64
	}{
		{"File size: 1.5 GB", 1.5},
		{"Downloaded 💾 750 MB", 750.0 / 1024.0},
		{"Archive size: 2 TB", 2048.0},
		{"Unknown size", 0.0},
	}

	for _, tt := range tests {
		got := parseFileSizeGB(tt.title)
		diff := got - tt.expected
		if diff < -0.01 || diff > 0.01 {
			t.Errorf("parseFileSizeGB(%q) = %f; want %f", tt.title, got, tt.expected)
		}
	}
}

func TestFormatFileSize(t *testing.T) {
	if got := formatFileSize(2.5); got != "2.50 GB" {
		t.Errorf("formatFileSize(2.5) = %q; want \"2.50 GB\"", got)
	}
	if got := formatFileSize(0.5); got != "512 MB" {
		t.Errorf("formatFileSize(0.5) = %q; want \"512 MB\"", got)
	}
	if got := formatFileSize(0); got != "" {
		t.Errorf("formatFileSize(0) = %q; want \"\"", got)
	}
}

func TestExtractMagnetInfo(t *testing.T) {
	rawMagnet := "magnet:?xt=urn:btih:424e82e489c401b6caebdeae6c2ad4230301b8e0&dn=Sattamum+Needhiyum+S02+1080p+WEB-DL+1.8GB&xl=1932735283"
	hash, title, quality, sizeGB := extractMagnetInfo(rawMagnet)

	if strings.ToLower(hash) != "424e82e489c401b6caebdeae6c2ad4230301b8e0" {
		t.Errorf("expected hash 424e82e489c401b6caebdeae6c2ad4230301b8e0, got %s", hash)
	}
	if !strings.Contains(title, "Sattamum") {
		t.Errorf("expected title to contain Sattamum, got %s", title)
	}
	if quality != "1080p" {
		t.Errorf("expected quality 1080p, got %s", quality)
	}
	if sizeGB <= 1.0 {
		t.Errorf("expected sizeGB > 1.0, got %f", sizeGB)
	}
}

func TestUnpackJS(t *testing.T) {
	// Dean Edwards packer sample
	packed := "eval(function(p,a,c,k,e,d){return p}('0 1 2',3,3,'hello|world|stream'.split('|')))"
	unpacked := UnpackJS(packed)
	if !strings.Contains(unpacked, "hello") || !strings.Contains(unpacked, "stream") {
		t.Errorf("UnpackJS failed to unpack tokens, got %q", unpacked)
	}
}
