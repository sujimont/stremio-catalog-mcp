package catalog

import "strings"

// RedisCatalogKey returns the profile-scoped Redis key for catalog data.
func RedisCatalogKey(profile string) string {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" || profile == "default" {
		return "stremio-catalog-mcp:v1:catalogs"
	}
	return "stremio-catalog-mcp:v1:catalogs:" + profile
}

// RedisVisibilityKey returns the Redis key for Home screen row visibility overrides.
func RedisVisibilityKey(profile string) string {
	return RedisCatalogKey(profile) + ":home-visibility"
}
