package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
	"github.com/amxv/stremio-catalog-mcp/pkg/crawler"
	"github.com/amxv/stremio-catalog-mcp/pkg/device"
	"github.com/amxv/stremio-catalog-mcp/pkg/streams"
	"github.com/amxv/stremio-catalog-mcp/pkg/stremio"
	"github.com/amxv/stremio-catalog-mcp/pkg/tmdb"
	"github.com/amxv/stremio-catalog-mcp/pkg/torbox"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Runtime struct {
	Manager         catalog.Manager
	Account         stremio.AccountAPI
	Store           *catalog.RedisStore
	VisibilityStore catalog.Store
	DeviceRegistry  *device.Registry
	BaseURL         string
	FeedToken       string
	TorboxAPIKey    string
	TorboxBaseURL   string
	TMDBAPIKey      string
	TVWebhookURL    string
}

func runtimeFromEnv() Runtime {
	store := &catalog.RedisStore{
		URL:   os.Getenv("UPSTASH_REDIS_REST_URL"),
		Token: os.Getenv("UPSTASH_REDIS_REST_TOKEN"),
		Key:   catalog.RedisCatalogKey(""),
	}
	visibilityStore := &catalog.RedisStore{
		URL:   store.URL,
		Token: store.Token,
		Key:   catalog.RedisVisibilityKey(""),
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
	deviceReg := device.NewRegistry(store.URL, store.Token)
	return Runtime{
		Manager:         catalog.Manager{Store: store, Resolver: catalog.Cinemeta{}},
		Account:         account,
		Store:           store,
		VisibilityStore: visibilityStore,
		DeviceRegistry:  deviceReg,
		BaseURL:         os.Getenv("PUBLIC_BASE_URL"),
		FeedToken:       os.Getenv("STREMIO_FEED_TOKEN"),
		TorboxAPIKey:    os.Getenv("TORBOX_API_KEY"),
		TMDBAPIKey:      os.Getenv("TMDB_API_KEY"),
		TVWebhookURL:    os.Getenv("TV_WEBHOOK_URL"),
	}
}

func (rt Runtime) forProfile(profile string) Runtime {
	profile = stremio.NormalizeProfile(profile)
	if profile == "" || profile == "default" {
		return rt
	}
	key := catalog.RedisCatalogKey(profile)
	store := &catalog.RedisStore{URL: rt.Store.URL, Token: rt.Store.Token, Key: key}
	visibilityStore := &catalog.RedisStore{URL: store.URL, Token: store.Token, Key: catalog.RedisVisibilityKey(profile)}
	return Runtime{
		Manager:         catalog.Manager{Store: store, Resolver: rt.Manager.Resolver},
		Account:         rt.Account,
		Store:           store,
		VisibilityStore: visibilityStore,
		DeviceRegistry:  rt.DeviceRegistry,
		BaseURL:         rt.BaseURL,
		FeedToken:       rt.FeedToken,
		TorboxAPIKey:    rt.TorboxAPIKey,
		TorboxBaseURL:   rt.TorboxBaseURL,
		TMDBAPIKey:      rt.TMDBAPIKey,
		TVWebhookURL:    rt.TVWebhookURL,
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

func readOnlyTool(name, description string) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}
}

func actionTool(name, description string, destructive bool) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &destructive,
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

type StreamAvailabilityInput struct {
	IMDbID    string   `json:"imdb_id,omitempty" jsonschema:"Single IMDb ID (e.g. tt1375666, or tt37539312:2:1 for series season/episode)"`
	IMDbIDs   []string `json:"imdb_ids,omitempty" jsonschema:"Optional list of up to 50 IMDb IDs to batch-check all at once in parallel"`
	Title     string   `json:"title,omitempty" jsonschema:"Optional title to verify stream filename match"`
	MediaType string   `json:"media_type,omitempty" jsonschema:"movie or series (default movie)"`
	Season    int      `json:"season,omitempty" jsonschema:"Season number for series (e.g. 2 for Season 2, defaults to 1)"`
	Episode   int      `json:"episode,omitempty" jsonschema:"Episode number for series (e.g. 1 for Episode 1, defaults to 1)"`
}

type TMDBDiscoverInput struct {
	Language      string  `json:"language,omitempty" jsonschema:"Regional language code or name (e.g. ml for Malayalam, ta for Tamil, hi for Hindi, ko for Korean, en for English)"`
	Genre         string  `json:"genre,omitempty" jsonschema:"Genre name (e.g. action, thriller, comedy, crime, romance, sci-fi, horror)"`
	WatchProvider string  `json:"watch_provider,omitempty" jsonschema:"Streaming OTT platform (e.g. netflix, prime, hotstar, apple)"`
	Region        string  `json:"region,omitempty" jsonschema:"Region code (defaults to IN for Indian regional cinema, US for Hollywood)"`
	MediaType     string  `json:"media_type,omitempty" jsonschema:"movie or series (default movie)"`
	Year          int     `json:"year,omitempty" jsonschema:"Release year filter (e.g. 2024, 2025)"`
	MinRating     float64 `json:"min_rating,omitempty" jsonschema:"Minimum TMDB rating (e.g. 7.0)"`
	SortBy        string  `json:"sort_by,omitempty" jsonschema:"Sort order (default popularity.desc, or vote_average.desc)"`
	Limit         int     `json:"limit,omitempty" jsonschema:"Maximum results to return (1-50, default 10)"`
}

type TMDBDiscoverResult struct {
	Items   []tmdb.Item `json:"items"`
	Count   int         `json:"count"`
	Message string      `json:"message,omitempty"`
}

type TMDBSimilarInput struct {
	IMDbID    string `json:"imdb_id,omitempty" jsonschema:"IMDb ID of movie/series to find recommendations for"`
	Title     string `json:"title,omitempty" jsonschema:"Title of movie/series to find recommendations for"`
	MediaType string `json:"media_type,omitempty" jsonschema:"movie or series (default movie)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum results to return (1-50, default 10)"`
}

type TMDBSimilarResult struct {
	Items   []tmdb.Item `json:"items"`
	Count   int         `json:"count"`
	Message string      `json:"message,omitempty"`
}

type MediaCrawlInput struct {
	Title      string  `json:"title,omitempty" jsonschema:"Movie or show title to crawl"`
	IMDbID     string  `json:"imdb_id,omitempty" jsonschema:"Optional IMDb ID for precision"`
	MediaType  string  `json:"media_type,omitempty" jsonschema:"movie or series (default movie)"`
	Season     int     `json:"season,omitempty" jsonschema:"Season number for series (e.g. 2 for Season 2)"`
	Episode    int     `json:"episode,omitempty" jsonschema:"Episode number for series (default 1)"`
	Magnet     string  `json:"magnet,omitempty" jsonschema:"Direct magnet URI (magnet:?xt=...) to verify and ingest immediately to TorBox"`
	PageURL    string  `json:"page_url,omitempty" jsonschema:"Torrent forum or webpage URL (e.g. 1TamilMV topic) to crawl, extract magnets, and ingest best release to TorBox"`
	MaxSizeGB  float64 `json:"max_size_gb,omitempty" jsonschema:"Maximum file size in GB to ingest to TorBox (default 10.0 GB per movie or episode)"`
	AutoIngest *bool   `json:"auto_ingest,omitempty" jsonschema:"Automatically ingest best torrent/stream to TorBox (default true)"`
}

type StreamUnmaskInput struct {
	URL              string `json:"url" jsonschema:"Masked webpage or embed player URL to unmask"`
	AutoIngestTorbox bool   `json:"auto_ingest_torbox,omitempty" jsonschema:"Automatically send unmasked video URL to TorBox WebDL"`
}

type StreamUnmaskResult struct {
	OriginalURL    string `json:"original_url"`
	UnmaskedURL    string `json:"unmasked_url"`
	StreamType     string `json:"stream_type"`
	RefererHeader  string `json:"referer_header,omitempty"`
	TorboxIngested bool   `json:"torbox_ingested,omitempty"`
	TorboxID       int    `json:"torbox_id,omitempty"`
	Message        string `json:"message"`
}

type AddonsManageInput struct {
	Action      string `json:"action" jsonschema:"Action to perform: list, install, or uninstall"`
	ManifestURL string `json:"manifest_url,omitempty" jsonschema:"Addon manifest URL for install action"`
	AddonID     string `json:"addon_id,omitempty" jsonschema:"Addon ID or transport URL for uninstall action"`
}

type AddonsManageResult struct {
	Addons  []stremio.InstalledAddonInfo `json:"addons,omitempty"`
	Count   int                          `json:"count,omitempty"`
	Message string                       `json:"message"`
}

type DevicesManageInput struct {
	Action     string `json:"action" jsonschema:"Action: list, register, remove, or reset"`
	Name       string `json:"name,omitempty" jsonschema:"Display name of the TV (e.g. Living Room TV, Bedroom TV)"`
	Alias      string `json:"alias,omitempty" jsonschema:"Room alias (e.g. living_room, bedroom, kids_room)"`
	IP         string `json:"ip,omitempty" jsonschema:"Local Wi-Fi IP address of the TV (e.g. 192.168.1.50)"`
	WebhookURL string `json:"webhook_url,omitempty" jsonschema:"Optional Home Assistant or TV webhook URL"`
	Platform   string `json:"platform,omitempty" jsonschema:"TV OS platform: android_tv, fire_tv, or home_assistant (default android_tv)"`
}

type DevicesManageResult struct {
	Devices []device.DeviceConfig `json:"devices,omitempty"`
	Count   int                   `json:"count,omitempty"`
	Message string                `json:"message"`
}

func serverFor(rt Runtime) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "stremio-catalog-mcp", Version: "0.6.0"}, nil)

	// Tool 1: catalogs_get
	mcp.AddTool(s, readOnlyTool("catalogs_get", "Read custom catalog rows and their ordered titles. Supports profile parameter for multi-profile isolation."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input CatalogsGetInput) (*mcp.CallToolResult, CatalogsGetResult, error) {
			result, err := rt.catalogsGet(ctx, input)
			return nil, result, err
		})

	// Tool 2: catalogs_update
	mcp.AddTool(s, actionTool("catalogs_update", "Batch mutate custom catalog rows: create, edit, delete, reorder titles, or prune watched items with CAS safety.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, input catalog.BatchInput) (*mcp.CallToolResult, CatalogsUpdateResult, error) {
			result, err := rt.catalogsUpdate(ctx, input)
			return nil, result, err
		})

	// Tool 3: watch_history_get
	mcp.AddTool(s, readOnlyTool("watch_history_get", "Fetch recently watched movies and series with progress percent and completion status from Stremio library."),
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

	// Tool 4: home_layout_get
	mcp.AddTool(s, readOnlyTool("home_layout_get", "List all installed Stremio addons and catalog rows with Home screen visibility and order."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, stremio.HomeLayout, error) {
			view, err := rt.homeLayoutGet(ctx)
			return nil, view, err
		})

	// Tool 5: home_layout_update
	mcp.AddTool(s, actionTool("home_layout_update", "Batch show/hide rows or reorder catalog rows and addons on the Stremio Home screen. Supports dry_run.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, input stremio.HomeUpdateInput) (*mcp.CallToolResult, HomeUpdateResult, error) {
			result, err := rt.homeLayoutUpdate(ctx, input)
			return nil, result, err
		})

	// Tool 6: stream_availability_check
	mcp.AddTool(s, readOnlyTool("stream_availability_check", "Verify torrent stream availability, quality, seeders, and TorBox cache status for a movie or TV series."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input StreamAvailabilityInput) (*mcp.CallToolResult, streams.StreamAvailabilityResult, error) {
			streamBase := rt.Account.GetTorrentioStreamBase(ctx)
			if len(input.IMDbIDs) > 0 {
				result, err := streams.CheckBatchAvailability(ctx, input.MediaType, input.IMDbIDs, rt.TorboxAPIKey, streamBase)
				return nil, result, err
			}
			imdbID := input.IMDbID
			if input.MediaType == "series" && imdbID != "" && !strings.Contains(imdbID, ":") {
				s := input.Season
				if s <= 0 {
					s = 1
				}
				ep := input.Episode
				if ep <= 0 {
					ep = 1
				}
				imdbID = fmt.Sprintf("%s:%d:%d", imdbID, s, ep)
			}
			result, err := streams.CheckAvailability(ctx, input.MediaType, imdbID, input.Title, rt.TorboxAPIKey, streamBase)
			return nil, result, err
		})

	// Tool 7: tmdb_discover
	mcp.AddTool(s, readOnlyTool("tmdb_discover", "Discover movies and TV series by regional language, genre, streaming provider, year, or rating."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input TMDBDiscoverInput) (*mcp.CallToolResult, TMDBDiscoverResult, error) {
			client := tmdb.NewClient(rt.TMDBAPIKey)
			items, err := client.Discover(ctx, tmdb.DiscoverOptions{
				MediaType:     input.MediaType,
				Language:      input.Language,
				Genre:         input.Genre,
				WatchProvider: input.WatchProvider,
				Region:        input.Region,
				Year:          input.Year,
				MinRating:     input.MinRating,
				SortBy:        input.SortBy,
				Limit:         input.Limit,
			})
			if err != nil {
				return nil, TMDBDiscoverResult{}, err
			}
			msg := fmt.Sprintf("Found %d titles matching discovery criteria.", len(items))
			if rt.TMDBAPIKey == "" {
				msg += " (Retrieved via Cinemeta catalog. Configure TMDB_API_KEY in Vercel to enable regional languages and OTT filters)."
			}
			return nil, TMDBDiscoverResult{Items: items, Count: len(items), Message: msg}, nil
		})

	// Tool 8: tmdb_similar
	mcp.AddTool(s, readOnlyTool("tmdb_similar", "Find similar or recommended movies and TV series for a given title or IMDb ID."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input TMDBSimilarInput) (*mcp.CallToolResult, TMDBSimilarResult, error) {
			client := tmdb.NewClient(rt.TMDBAPIKey)
			items, err := client.Similar(ctx, tmdb.SimilarOptions{
				IMDbID:    input.IMDbID,
				Title:     input.Title,
				MediaType: input.MediaType,
				Limit:     input.Limit,
			})
			if err != nil {
				return nil, TMDBSimilarResult{}, err
			}
			msg := fmt.Sprintf("Found %d similar titles.", len(items))
			return nil, TMDBSimilarResult{Items: items, Count: len(items), Message: msg}, nil
		})

	// Tool 9: media_crawl_and_ingest
	mcp.AddTool(s, actionTool("media_crawl_and_ingest", "Crawl torrent forum pages, ingest direct magnets, or search torrents under size limit and ingest to TorBox.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, input MediaCrawlInput) (*mcp.CallToolResult, *crawler.CrawlResult, error) {
			streamBase := rt.Account.GetTorrentioStreamBase(ctx)
			autoIngest := true
			if input.AutoIngest != nil {
				autoIngest = *input.AutoIngest
			}
			opts := crawler.CrawlOptions{
				Title:      input.Title,
				IMDbID:     input.IMDbID,
				MediaType:  input.MediaType,
				Season:     input.Season,
				Episode:    input.Episode,
				Magnet:     input.Magnet,
				PageURL:    input.PageURL,
				TorboxKey:  rt.TorboxAPIKey,
				MaxSizeGB:  input.MaxSizeGB,
				AutoIngest: autoIngest,
				StreamBase: streamBase,
			}
			result, err := crawler.CrawlAndIngest(ctx, opts)
			return nil, result, err
		})

	// Tool 10: stream_unmask
	mcp.AddTool(s, actionTool("stream_unmask", "Resolve masked embed players and streaming webpages into direct video URLs with optional TorBox WebDL ingestion.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, input StreamUnmaskInput) (*mcp.CallToolResult, StreamUnmaskResult, error) {
			unmasked, err := crawler.UnmaskStream(ctx, nil, input.URL)
			if err != nil {
				return nil, StreamUnmaskResult{}, err
			}
			res := StreamUnmaskResult{
				OriginalURL:   unmasked.OriginalURL,
				UnmaskedURL:   unmasked.UnmaskedURL,
				StreamType:    unmasked.StreamType,
				RefererHeader: unmasked.RefererHeader,
				Message:       "Successfully unmasked raw " + unmasked.StreamType + " video stream.",
			}
			if input.AutoIngestTorbox && rt.TorboxAPIKey != "" {
				tb := torbox.NewClient(rt.TorboxAPIKey)
				dl, err := tb.AddWebDownload(ctx, unmasked.UnmaskedURL, "")
				if err != nil {
					res.Message += fmt.Sprintf(" Warning: TorBox WebDL failed: %v", err)
				} else if dl != nil {
					res.TorboxIngested = true
					res.TorboxID = dl.Data.DownloadID
					res.Message += fmt.Sprintf(" Ingested to TorBox WebDL (ID: %d).", res.TorboxID)
				}
			}
			return nil, res, nil
		})

	// Tool 11: play_on_tv
	mcp.AddTool(s, actionTool("play_on_tv", "Generate Stremio deep links, web links, or trigger Home Assistant TV webhooks for playback.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, input device.PlayOnTVInput) (*mcp.CallToolResult, *device.PlayOnTVResult, error) {
			if input.WebhookURL == "" {
				input.WebhookURL = rt.TVWebhookURL
			}
			res, err := device.TriggerPlay(ctx, rt.DeviceRegistry, input)
			return nil, res, err
		})

	// Tool 12: addons_manage
	mcp.AddTool(s, actionTool("addons_manage", "List installed addons, install new addons from manifest URL, or safely uninstall addons from your Stremio account.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, input AddonsManageInput) (*mcp.CallToolResult, AddonsManageResult, error) {
			if rt.Account.AuthKey == "" {
				return nil, AddonsManageResult{Message: "Stremio account is not linked. Configure STREMIO_AUTH_KEY to manage addons."}, nil
			}
			switch input.Action {
			case "list":
				list, err := rt.Account.ListInstalledAddons(ctx)
				if err != nil {
					return nil, AddonsManageResult{}, err
				}
				return nil, AddonsManageResult{Addons: list, Count: len(list), Message: fmt.Sprintf("Found %d installed addons.", len(list))}, nil
			case "install":
				if input.ManifestURL == "" {
					return nil, AddonsManageResult{}, errors.New("manifest_url is required for install action")
				}
				info, err := rt.Account.InstallAddon(ctx, input.ManifestURL)
				if err != nil {
					return nil, AddonsManageResult{}, err
				}
				return nil, AddonsManageResult{Addons: []stremio.InstalledAddonInfo{*info}, Message: fmt.Sprintf("Successfully installed addon %q (%s).", info.Name, info.ID)}, nil
			case "uninstall":
				if input.AddonID == "" {
					return nil, AddonsManageResult{}, errors.New("addon_id is required for uninstall action")
				}
				if err := rt.Account.UninstallAddon(ctx, input.AddonID); err != nil {
					return nil, AddonsManageResult{}, err
				}
				return nil, AddonsManageResult{Message: fmt.Sprintf("Successfully uninstalled addon %q.", input.AddonID)}, nil
			default:
				return nil, AddonsManageResult{}, fmt.Errorf("unknown action %q (must be list, install, or uninstall)", input.Action)
			}
		})

	// Tool 13: devices_manage
	mcp.AddTool(s, actionTool("devices_manage", "List, register, or remove household TV targets and webhook configurations.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, input DevicesManageInput) (*mcp.CallToolResult, DevicesManageResult, error) {
			reg := rt.DeviceRegistry
			if reg == nil {
				reg = device.NewRegistryFromEnv()
			}
			switch input.Action {
			case "list":
				list, err := reg.List(ctx)
				if err != nil {
					return nil, DevicesManageResult{}, err
				}
				return nil, DevicesManageResult{Devices: list, Count: len(list), Message: fmt.Sprintf("Found %d registered household TV devices.", len(list))}, nil
			case "register":
				if input.Name == "" && input.Alias == "" {
					return nil, DevicesManageResult{}, errors.New("device name or alias is required")
				}
				dev := device.DeviceConfig{
					Name:       input.Name,
					Alias:      input.Alias,
					IP:         input.IP,
					WebhookURL: input.WebhookURL,
					Platform:   input.Platform,
				}
				if dev.Name == "" {
					dev.Name = dev.Alias
				}
				if err := reg.Register(ctx, dev); err != nil {
					return nil, DevicesManageResult{}, err
				}
				list, _ := reg.List(ctx)
				return nil, DevicesManageResult{Devices: list, Count: len(list), Message: fmt.Sprintf("Successfully registered TV device %q (%s).", dev.Name, dev.Alias)}, nil
			case "remove":
				if input.Alias == "" {
					return nil, DevicesManageResult{}, errors.New("alias is required to remove a device")
				}
				if err := reg.Remove(ctx, input.Alias); err != nil {
					return nil, DevicesManageResult{}, err
				}
				list, _ := reg.List(ctx)
				return nil, DevicesManageResult{Devices: list, Count: len(list), Message: fmt.Sprintf("Successfully removed TV device alias %q.", input.Alias)}, nil
			case "reset":
				if err := reg.Reset(ctx); err != nil {
					return nil, DevicesManageResult{}, err
				}
				list, _ := reg.List(ctx)
				if len(list) > 0 {
					return nil, DevicesManageResult{
						Devices: list,
						Count:   len(list),
						Message: fmt.Sprintf("Successfully reset device registry. Reverted to %d default device(s) from TV_DEVICES_JSON.", len(list)),
					}, nil
				}
				return nil, DevicesManageResult{
					Devices: []device.DeviceConfig{},
					Count:   0,
					Message: "Successfully reset device registry (0 devices configured).",
				}, nil
			default:
				return nil, DevicesManageResult{}, fmt.Errorf("unknown action %q (must be list, register, remove, or reset)", input.Action)
			}
		})

	// Tool 14: torbox_uploads_get
	mcp.AddTool(s, readOnlyTool("torbox_uploads_get", "List torrents and web downloads stored in your TorBox cloud account, including status, size, progress, and file breakdown."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input TorboxUploadsGetInput) (*mcp.CallToolResult, TorboxUploadsGetResult, error) {
			result, err := rt.torboxUploadsGet(ctx, input)
			return nil, result, err
		})

	// Tool 15: torbox_catalog_sync
	mcp.AddTool(s, actionTool("torbox_catalog_sync", "Sync completed TorBox uploads into a Stremio custom catalog row, resolving titles with Cinemeta/IMDb.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, input TorboxCatalogSyncInput) (*mcp.CallToolResult, TorboxCatalogSyncResult, error) {
			result, err := rt.torboxCatalogSync(ctx, input)
			return nil, result, err
		})

	return s
}
