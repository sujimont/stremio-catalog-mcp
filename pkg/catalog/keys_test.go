package catalog

import "testing"

func TestNormalizeProfile(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"   ", ""},
		{"default", "default"},
		{"DEFAULT", "default"},
		{"Kids", "kids"},
		{"Living-Room_1", "living-room_1"},
		{"default:home-visibility", "defaulthome-visibility"},
		{"x:account-sync", "xaccount-sync"},
		{"../../etc/passwd", "etcpasswd"},
		{"hello world!", "helloworld"},
		{"profile#1?test=1", "profile1test1"},
	}

	for _, tt := range tests {
		got := NormalizeProfile(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeProfile(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

func TestRedisCatalogKey(t *testing.T) {
	tests := []struct {
		profile string
		wantKey string
	}{
		{"", "stremio-catalog-mcp:v1:catalogs"},
		{"   ", "stremio-catalog-mcp:v1:catalogs"},
		{"default", "stremio-catalog-mcp:v1:catalogs"},
		{"Default", "stremio-catalog-mcp:v1:catalogs"},
		{"kids", "stremio-catalog-mcp:v1:catalogs:kids"},
		{"guest_room", "stremio-catalog-mcp:v1:catalogs:guest_room"},
		{"default:home-visibility", "stremio-catalog-mcp:v1:catalogs:defaulthome-visibility"},
		{"x:account-sync", "stremio-catalog-mcp:v1:catalogs:xaccount-sync"},
	}

	for _, tt := range tests {
		got := RedisCatalogKey(tt.profile)
		if got != tt.wantKey {
			t.Errorf("RedisCatalogKey(%q) = %q; want %q", tt.profile, got, tt.wantKey)
		}
	}
}

func TestRedisVisibilityKey(t *testing.T) {
	got := RedisVisibilityKey("kids")
	want := "stremio-catalog-mcp:v1:catalogs:kids:home-visibility"
	if got != want {
		t.Errorf("RedisVisibilityKey(\"kids\") = %q; want %q", got, want)
	}

	gotDefault := RedisVisibilityKey("")
	wantDefault := "stremio-catalog-mcp:v1:catalogs:home-visibility"
	if gotDefault != wantDefault {
		t.Errorf("RedisVisibilityKey(\"\") = %q; want %q", gotDefault, wantDefault)
	}
}
