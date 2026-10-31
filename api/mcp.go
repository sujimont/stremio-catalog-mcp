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
			p = catalog.NormalizeProfile(p)
			store := &catalog.RedisStore{
				URL:   os.Getenv("UPSTASH_REDIS_REST_URL"),
				Token: os.Getenv("UPSTASH_REDIS_REST_TOKEN"),
				Key:   catalog.RedisCatalogKey(p),
			}
			return catalog.Manager{Store: store, Resolver: catalog.Cinemeta{}}
		}
		defaultMgr := mgrForProfile("")
		stremio.Handler{
			Manager:           defaultMgr,
			ManagerForProfile: mgrForProfile,
			FeedToken:         os.Getenv("STREMIO_FEED_TOKEN"),
			TorboxAPIKey:      os.Getenv("TORBOX_API_KEY"),
		}.ServeHTTP(w, r)
		return
	}
	server.Handler().ServeHTTP(w, r)
}
