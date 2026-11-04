package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
	"github.com/amxv/stremio-catalog-mcp/pkg/server"
	"github.com/amxv/stremio-catalog-mcp/pkg/stremio"
	"github.com/amxv/stremio-catalog-mcp/pkg/syncer"
)

// Handler exposes a stateless MCP endpoint as a Vercel Go Function.
func Handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("resource") == "cron" || strings.HasPrefix(r.URL.Path, "/api/cron") {
		secret := strings.TrimSpace(os.Getenv("MCP_API_KEY"))
		cronSecret := strings.TrimSpace(os.Getenv("CRON_SECRET"))
		supplied := r.URL.Query().Get("key")
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			supplied = strings.TrimSpace(auth[7:])
		}
		authOK := false
		if secret != "" && supplied == secret {
			authOK = true
		}
		if cronSecret != "" && supplied == cronSecret {
			authOK = true
		}
		if !authOK && (strings.HasPrefix(r.Header.Get("User-Agent"), "vercel-cron/") ||
			r.Header.Get("x-vercel-cron") == "1" ||
			r.Header.Get("x-vercel-cron-schedule") != "") {
			authOK = true
		}
		if !authOK {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		store := &catalog.RedisStore{
			URL:   os.Getenv("UPSTASH_REDIS_REST_URL"),
			Token: os.Getenv("UPSTASH_REDIS_REST_TOKEN"),
			Key:   catalog.RedisCatalogKey(""),
		}
		account := stremio.AccountAPI{
			AuthKey:       os.Getenv("STREMIO_AUTH_KEY"),
			TorboxEnabled: os.Getenv("TORBOX_API_KEY") != "",
		}
		account.BeforeReplace = func(ctx context.Context, addons []stremio.Descriptor) error {
			raw, err := json.Marshal(addons)
			if err != nil {
				return err
			}
			return store.SaveAddonBackup(ctx, raw)
		}

		err := syncer.SyncAll(r.Context(), store, account, os.Getenv("PUBLIC_BASE_URL"), os.Getenv("STREMIO_FEED_TOKEN"))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"error":%q}`, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","message":"all 6 Indian catalogs successfully synced"}`))
		return
	}

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
