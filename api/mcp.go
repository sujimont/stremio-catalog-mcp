package handler

import (
	"net/http"
	"os"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
	"github.com/amxv/stremio-catalog-mcp/pkg/server"
	"github.com/amxv/stremio-catalog-mcp/pkg/stremio"
)

// Handler exposes a stateless MCP endpoint as a Vercel Go Function.
func Handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("resource") != "" {
		mgrForProfile := func(p string) catalog.Manager {
			p = stremio.NormalizeProfile(p)
			key := "stremio-catalog-mcp:v1:catalogs"
			if p != "" && p != "default" {
				key = "stremio-catalog-mcp:v1:catalogs:" + p
			}
			store := &catalog.RedisStore{
				URL:   os.Getenv("UPSTASH_REDIS_REST_URL"),
				Token: os.Getenv("UPSTASH_REDIS_REST_TOKEN"),
				Key:   key,
			}
			return catalog.Manager{Store: store, Resolver: catalog.Cinemeta{}}
		}
		defaultMgr := mgrForProfile("")
		stremio.Handler{
			Manager:           defaultMgr,
			ManagerForProfile: mgrForProfile,
			FeedToken:         os.Getenv("STREMIO_FEED_TOKEN"),
		}.ServeHTTP(w, r)
		return
	}
	server.Handler().ServeHTTP(w, r)
}
