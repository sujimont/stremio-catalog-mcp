package catalog

import "strings"

// NormalizeProfile cleans and sanitizes a profile identifier, allowing only [a-z0-9_-].
func NormalizeProfile(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	var clean strings.Builder
	for _, r := range p {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			clean.WriteRune(r)
		}
	}
	return clean.String()
}

// RedisCatalogKey returns the profile-scoped Redis key for catalog data.
// It normalizes the profile to prevent Redis key injection and namespace pollution.
func RedisCatalogKey(profile string) string {
	profile = NormalizeProfile(profile)
	if profile == "" || profile == "default" {
		return "stremio-catalog-mcp:v1:catalogs"
	}
	return "stremio-catalog-mcp:v1:catalogs:" + profile
}

// RedisVisibilityKey returns the Redis key for Home screen row visibility overrides.
func RedisVisibilityKey(profile string) string {
	return RedisCatalogKey(profile) + ":home-visibility"
}

