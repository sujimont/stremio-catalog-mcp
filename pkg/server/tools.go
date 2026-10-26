package server

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
	"github.com/amxv/stremio-catalog-mcp/pkg/stremio"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Runtime struct {
	Manager         catalog.Manager
	Account         stremio.AccountAPI
	Store           *catalog.RedisStore
	VisibilityStore catalog.Store
	BaseURL         string
	FeedToken       string
}

func runtimeFromEnv() Runtime {
	store := &catalog.RedisStore{URL: os.Getenv("UPSTASH_REDIS_REST_URL"), Token: os.Getenv("UPSTASH_REDIS_REST_TOKEN"), Key: "stremio-catalog-mcp:v1:catalogs"}
	visibilityStore := &catalog.RedisStore{URL: store.URL, Token: store.Token, Key: store.Key + ":home-visibility"}
	account := stremio.AccountAPI{AuthKey: os.Getenv("STREMIO_AUTH_KEY")}
	account.BeforeReplace = func(ctx context.Context, addons []stremio.Descriptor) error {
		raw, err := json.Marshal(addons)
		if err != nil {
			return err
		}
		return store.SaveAddonBackup(ctx, raw)
	}
	return Runtime{
		Manager: catalog.Manager{Store: store, Resolver: catalog.Cinemeta{}},
		Account: account,
		Store:   store, VisibilityStore: visibilityStore, BaseURL: os.Getenv("PUBLIC_BASE_URL"), FeedToken: os.Getenv("STREMIO_FEED_TOKEN"),
	}
}

func (rt Runtime) forProfile(profile string) Runtime {
	profile = stremio.NormalizeProfile(profile)
	if profile == "" || profile == "default" {
		return rt
	}
	key := "stremio-catalog-mcp:v1:catalogs:" + profile
	store := &catalog.RedisStore{URL: rt.Store.URL, Token: rt.Store.Token, Key: key}
	visibilityStore := &catalog.RedisStore{URL: store.URL, Token: store.Token, Key: key + ":home-visibility"}
	return Runtime{
		Manager:         catalog.Manager{Store: store, Resolver: rt.Manager.Resolver},
		Account:         rt.Account,
		Store:           store,
		VisibilityStore: visibilityStore,
		BaseURL:         rt.BaseURL,
		FeedToken:       rt.FeedToken,
	}
}

type SyncStatus struct {
	Synced  bool   `json:"synced"`
	Message string `json:"message"`
}

func (rt Runtime) sync(ctx context.Context) SyncStatus {
	if rt.Account.AuthKey == "" {
		return SyncStatus{Message: "Saved. Stremio account not linked; install the addon URL manually or configure STREMIO_AUTH_KEY to enable automatic row syncing."}
	}
	if rt.Store != nil {
		lockToken, err := catalog.NewID()
		if err != nil {
			return SyncStatus{Message: "Saved but sync lock failed"}
		}
		locked, err := rt.Store.AcquireSyncLock(ctx, lockToken)
		if err != nil {
			return SyncStatus{Message: "Saved but sync lock unavailable: " + err.Error()}
		}
		if !locked {
			return SyncStatus{Message: "Saved; another account sync is running. Retry with catalogs_update using a sync action."}
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = rt.Store.ReleaseSyncLock(releaseCtx, lockToken)
		}()
	}
	state, err := rt.Manager.Read(ctx)
	if err != nil {
		return SyncStatus{Message: "Saved but state could not be read for sync: " + err.Error()}
	}
	url, err := stremio.ManifestURL(rt.BaseURL, rt.FeedToken, state.Revision)
	if err != nil {
		return SyncStatus{Message: "Saved but cannot sync: " + err.Error()}
	}
	if err := rt.Account.Sync(ctx, state, url); err != nil {
		return SyncStatus{Message: "Saved but Stremio account sync failed: " + err.Error()}
	}
	return SyncStatus{Synced: true, Message: "Catalog manifest synced to Stremio. Restart or refresh the TV app if changes are not immediately visible."}
}

func makeServer() *mcp.Server { return serverFor(runtimeFromEnv()) }

// catalogTool centralizes the MCP metadata contract. These annotations are
// advisory discovery hints, not authorization controls: catalog write tools
// still modify state and require the configured MCP API key.
func catalogTool(name, description string) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
			// Do not set OpenWorldHint on any tool.
		},
	}
}

type WatchHistoryInput struct {
	Limit         int  `json:"limit,omitempty" jsonschema:"Maximum number of items to return (1-50, default 10)"`
	OnlyCompleted bool `json:"only_completed,omitempty" jsonschema:"Only return titles that have been fully watched"`
}

type WatchHistoryResult struct {
	Items   []stremio.WatchedItem `json:"items"`
	Count   int                   `json:"count"`
	Message string                `json:"message,omitempty"`
}

func serverFor(rt Runtime) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "stremio-catalog-mcp", Version: "0.4.0"}, nil)

	mcp.AddTool(s, catalogTool("catalogs_get", "Read custom catalog rows. Supports optional profile parameter (e.g. kids, suji, default). With no parameters return compact row summaries; pass row_id for one row's ordered titles including saved poster URLs, or include_titles=true for every row's titles. include_install_url=true returns the private addon URL for Stremio/Nuvio. include_pruned=true includes archived watched titles."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input CatalogsGetInput) (*mcp.CallToolResult, CatalogsGetResult, error) {
			result, err := rt.catalogsGet(ctx, input)
			return nil, result, err
		})

	mcp.AddTool(s, catalogTool("catalogs_update", "Batch changes to custom catalog CONTENT: actions create (name, type, optional titles, optional auto_prune), edit (row_id plus name/add_titles/remove_title_ids/replace_titles/move_titles/auto_prune/restore_title_ids/prune_title_ids), delete (row_id), sync (retry account sync). Supports profile parameter for Nuvio/multi-profile isolation. restore_title_ids restores archived titles; prune_title_ids reversibly archives watched titles."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input catalog.BatchInput) (*mcp.CallToolResult, CatalogsUpdateResult, error) {
			result, err := rt.catalogsUpdate(ctx, input)
			return nil, result, err
		})

	mcp.AddTool(s, catalogTool("watch_history_get", "Fetch recently watched movies and series from your Stremio library. Returns titles, IMDb IDs, poster URLs, and watch progress. Perfect for generating 'Because You Watched' recommendations or auto-pruning."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input WatchHistoryInput) (*mcp.CallToolResult, WatchHistoryResult, error) {
			if rt.Account.AuthKey == "" {
				return nil, WatchHistoryResult{Message: "Stremio account is not linked. Configure STREMIO_AUTH_KEY to read watch history."}, nil
			}
			items, err := rt.Account.GetWatchHistory(ctx, input.Limit, input.OnlyCompleted)
			if err != nil {
				return nil, WatchHistoryResult{}, err
			}
			return nil, WatchHistoryResult{Items: items, Count: len(items)}, nil
		})

	mcp.AddTool(s, catalogTool("home_layout_get", "Read the live Stremio Home layout: installed addon order, all catalogs including hidden rows, and supported visibility controls. Excludes private addon URLs. If the account is not linked, shows custom rows only."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, stremio.HomeLayout, error) {
			view, err := rt.homeLayoutGet(ctx)
			return nil, view, err
		})

	mcp.AddTool(s, catalogTool("home_layout_update", "Batch hide/show Home rows, reorder rows within any installed addon, and move complete addons. Provide exact IDs from home_layout_get. Use dry_run=true to preview. Preserves stream addons and backs up the account before saving."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input stremio.HomeUpdateInput) (*mcp.CallToolResult, HomeUpdateResult, error) {
			result, err := rt.homeLayoutUpdate(ctx, input)
			return nil, result, err
		})
	return s
}
