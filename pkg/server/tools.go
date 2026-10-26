package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	TMDBAPIKey      string
	TVWebhookURL    string
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
	key := "stremio-catalog-mcp:v1:catalogs:" + profile
	store := &catalog.RedisStore{URL: rt.Store.URL, Token: rt.Store.Token, Key: key}
	visibilityStore := &catalog.RedisStore{URL: store.URL, Token: store.Token, Key: key + ":home-visibility"}
	return Runtime{
		Manager:         catalog.Manager{Store: store, Resolver: rt.Manager.Resolver},
		Account:         rt.Account,
		Store:           store,
		VisibilityStore: visibilityStore,
		DeviceRegistry:  rt.DeviceRegistry,
		BaseURL:         rt.BaseURL,
		FeedToken:       rt.FeedToken,
		TorboxAPIKey:    rt.TorboxAPIKey,
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

func makeServer() *mcp.Server { return serverFor(runtimeFromEnv()) }

// catalogTool centralizes the MCP metadata contract. These annotations are
// advisory discovery hints, not authorization controls.
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

type StreamAvailabilityInput struct {
	IMDbID       string `json:"imdb_id" jsonschema:"IMDb ID (e.g. tt1375666)"`
	MediaType    string `json:"media_type,omitempty" jsonschema:"movie or series (default movie)"`
	TorboxAPIKey string `json:"torbox_api_key,omitempty" jsonschema:"Optional TorBox API key override to verify instant cloud caching"`
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
	Limit         int     `json:"limit,omitempty" jsonschema:"Maximum results to return (1-20, default 10)"`
	TMDBAPIKey   string  `json:"tmdb_api_key,omitempty" jsonschema:"Optional TMDB API key override"`
}

type TMDBDiscoverResult struct {
	Items   []tmdb.Item `json:"items"`
	Count   int         `json:"count"`
	Message string      `json:"message,omitempty"`
}

type TMDBSimilarInput struct {
	IMDbID      string `json:"imdb_id,omitempty" jsonschema:"IMDb ID of movie/series to find recommendations for"`
	Title       string `json:"title,omitempty" jsonschema:"Title of movie/series to find recommendations for"`
	MediaType   string `json:"media_type,omitempty" jsonschema:"movie or series (default movie)"`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum results to return (1-20, default 10)"`
	TMDBAPIKey string `json:"tmdb_api_key,omitempty" jsonschema:"Optional TMDB API key override"`
}

type TMDBSimilarResult struct {
	Items   []tmdb.Item `json:"items"`
	Count   int         `json:"count"`
	Message string      `json:"message,omitempty"`
}

type MediaCrawlInput struct {
	Title        string `json:"title" jsonschema:"Movie or show title to crawl"`
	IMDbID       string `json:"imdb_id,omitempty" jsonschema:"Optional IMDb ID for precision"`
	MediaType    string `json:"media_type,omitempty" jsonschema:"movie or series (default movie)"`
	AutoIngest   bool   `json:"auto_ingest,omitempty" jsonschema:"Automatically ingest best torrent/stream to TorBox (default true)"`
	TorboxAPIKey string `json:"torbox_api_key,omitempty" jsonschema:"Optional TorBox API key override"`
}

type StreamUnmaskInput struct {
	URL              string `json:"url" jsonschema:"Masked webpage or embed player URL to unmask"`
	AutoIngestTorbox bool   `json:"auto_ingest_torbox,omitempty" jsonschema:"Automatically send unmasked video URL to TorBox WebDL"`
	TorboxAPIKey     string `json:"torbox_api_key,omitempty" jsonschema:"Optional TorBox API key override"`
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
	Action     string `json:"action" jsonschema:"Action: list, register, or remove"`
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
	s := mcp.NewServer(&mcp.Implementation{Name: "stremio-catalog-mcp", Version: "0.5.0"}, nil)

	// Tool 1: catalogs_get
	mcp.AddTool(s, catalogTool("catalogs_get", "Read custom catalog rows. Supports optional profile parameter (e.g. kids, suji, default). With no parameters return compact row summaries; pass row_id for one row's ordered titles including saved poster URLs, or include_titles=true for every row's titles. include_install_url=true returns the private addon URL for Stremio/Nuvio. include_pruned=true includes archived watched titles."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input CatalogsGetInput) (*mcp.CallToolResult, CatalogsGetResult, error) {
			result, err := rt.catalogsGet(ctx, input)
			return nil, result, err
		})

	// Tool 2: catalogs_update
	mcp.AddTool(s, catalogTool("catalogs_update", "Batch changes to custom catalog CONTENT: actions create (name, type, optional titles, optional auto_prune), edit (row_id plus name/add_titles/remove_title_ids/replace_titles/move_titles/auto_prune/restore_title_ids/prune_title_ids), delete (row_id), sync (retry account sync). Supports profile parameter for Nuvio/multi-profile isolation. restore_title_ids restores archived titles; prune_title_ids reversibly archives watched titles."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input catalog.BatchInput) (*mcp.CallToolResult, CatalogsUpdateResult, error) {
			result, err := rt.catalogsUpdate(ctx, input)
			return nil, result, err
		})

	// Tool 3: watch_history_get
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

	// Tool 4: home_layout_get
	mcp.AddTool(s, catalogTool("home_layout_get", "Read the live Stremio Home layout: installed addon order, all catalogs including hidden rows, and supported visibility controls. Excludes private addon URLs. If the account is not linked, shows custom rows only."),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, stremio.HomeLayout, error) {
			view, err := rt.homeLayoutGet(ctx)
			return nil, view, err
		})

	// Tool 5: home_layout_update
	mcp.AddTool(s, catalogTool("home_layout_update", "Batch hide/show Home rows, reorder rows within any installed addon, and move complete addons. Provide exact IDs from home_layout_get. Use dry_run=true to preview. Preserves stream addons and backs up the account before saving."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input stremio.HomeUpdateInput) (*mcp.CallToolResult, HomeUpdateResult, error) {
			result, err := rt.homeLayoutUpdate(ctx, input)
			return nil, result, err
		})

	// Tool 6: stream_availability_check
	mcp.AddTool(s, catalogTool("stream_availability_check", "⚡ Stream Availability Pre-Checker: verify active stream count, highest quality (4K/1080p/720p), max seeders, and instant TorBox cloud-cached status for any movie or series before adding to a catalog."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input StreamAvailabilityInput) (*mcp.CallToolResult, streams.StreamAvailabilityResult, error) {
			torboxKey := input.TorboxAPIKey
			if torboxKey == "" {
				torboxKey = rt.TorboxAPIKey
			}
			streamBase, discoveredTBKey := rt.Account.GetTorrentioStreamBase(ctx)
			if torboxKey == "" && discoveredTBKey != "" {
				torboxKey = discoveredTBKey
			}
			result, err := streams.CheckAvailability(ctx, input.MediaType, input.IMDbID, torboxKey, streamBase)
			return nil, result, err
		})

	// Tool 7: tmdb_discover
	mcp.AddTool(s, catalogTool("tmdb_discover", "🌍 TMDB Deep Discovery & Regional Cinema: discover high-rated titles by regional language (Malayalam, Tamil, Hindi, Telugu, Korean, etc.), OTT provider (Netflix, Prime, Hotstar), genre, year, and min rating."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input TMDBDiscoverInput) (*mcp.CallToolResult, TMDBDiscoverResult, error) {
			apiKey := input.TMDBAPIKey
			if apiKey == "" {
				apiKey = rt.TMDBAPIKey
			}
			client := tmdb.NewClient(apiKey)
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
			if apiKey == "" {
				msg += " (Retrieved via Cinemeta catalog. Configure TMDB_API_KEY in Vercel to enable regional languages and OTT filters)."
			}
			return nil, TMDBDiscoverResult{Items: items, Count: len(items), Message: msg}, nil
		})

	// Tool 8: tmdb_similar
	mcp.AddTool(s, catalogTool("tmdb_similar", "🎬 Similar Movie & TV Deep Dive: find similar and recommended movies/series for any title or IMDb ID, ready to create dynamic Stremio or Nuvio rows."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input TMDBSimilarInput) (*mcp.CallToolResult, TMDBSimilarResult, error) {
			apiKey := input.TMDBAPIKey
			if apiKey == "" {
				apiKey = rt.TMDBAPIKey
			}
			client := tmdb.NewClient(apiKey)
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
	mcp.AddTool(s, catalogTool("media_crawl_and_ingest", "🚀 Autonomous TorBox Torrent & HTTP Crawler: searches high-quality torrents/magnets, verifies TorBox cache, and automatically ingests the best stream to TorBox so it's ready to stream."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input MediaCrawlInput) (*mcp.CallToolResult, *crawler.CrawlResult, error) {
			torboxKey := input.TorboxAPIKey
			if torboxKey == "" {
				torboxKey = rt.TorboxAPIKey
			}
			streamBase, discoveredTBKey := rt.Account.GetTorrentioStreamBase(ctx)
			if torboxKey == "" && discoveredTBKey != "" {
				torboxKey = discoveredTBKey
			}
			result, err := crawler.CrawlAndIngest(ctx, input.Title, input.IMDbID, input.MediaType, torboxKey, input.AutoIngest, streamBase)
			return nil, result, err
		})

	// Tool 10: stream_unmask
	mcp.AddTool(s, catalogTool("stream_unmask", "🔓 Movierulz Stream Unmasker: resolves masked embed players and streaming webpages into raw, directly playable .mp4 or .m3u8 URLs, with optional TorBox WebDL ingestion."),
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
			tKey := input.TorboxAPIKey
			if tKey == "" {
				tKey = rt.TorboxAPIKey
			}
			if input.AutoIngestTorbox && tKey != "" {
				tb := torbox.NewClient(tKey)
				dl, err := tb.AddWebDownload(ctx, unmasked.UnmaskedURL, "")
				if err == nil && dl != nil {
					res.TorboxIngested = true
					res.TorboxID = dl.Data.DownloadID
					res.Message += fmt.Sprintf(" Ingested to TorBox WebDL (ID: %d).", res.TorboxID)
				}
			}
			return nil, res, nil
		})

	// Tool 11: play_on_tv
	mcp.AddTool(s, catalogTool("play_on_tv", "📺 Play On TV Remote Trigger: generates targeted Stremio deep-links, web-links, and ADB commands for any TV in the house (Living Room, Bedroom, Kids Room), with optional Home Assistant/Android TV webhook triggers."),
		func(ctx context.Context, _ *mcp.CallToolRequest, input device.PlayOnTVInput) (*mcp.CallToolResult, *device.PlayOnTVResult, error) {
			if input.WebhookURL == "" {
				input.WebhookURL = rt.TVWebhookURL
			}
			res, err := device.TriggerPlay(ctx, rt.DeviceRegistry, input)
			return nil, res, err
		})

	// Tool 12: addons_manage
	mcp.AddTool(s, catalogTool("addons_manage", "🧩 One-Click Addon Manager via Chat: list installed addons, install new addons from manifest URL, or safely uninstall addons from your Stremio account."),
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
	mcp.AddTool(s, catalogTool("devices_manage", "🏠 Household TV Devices Manager: list, register, or remove smart TVs in your house (Living Room, Bedroom, Kids Room) with their local Wi-Fi IP address or webhook so Play On TV knows exactly which screen to launch."),
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
				return nil, DevicesManageResult{Devices: list, Count: len(list), Message: fmt.Sprintf("Successfully registered TV device %q (%s) with IP %s.", dev.Name, dev.Alias, dev.IP)}, nil
			case "remove":
				if input.Alias == "" {
					return nil, DevicesManageResult{}, errors.New("alias is required to remove a device")
				}
				if err := reg.Remove(ctx, input.Alias); err != nil {
					return nil, DevicesManageResult{}, err
				}
				list, _ := reg.List(ctx)
				return nil, DevicesManageResult{Devices: list, Count: len(list), Message: fmt.Sprintf("Successfully removed TV device alias %q.", input.Alias)}, nil
			default:
				return nil, DevicesManageResult{}, fmt.Errorf("unknown action %q (must be list, register, or remove)", input.Action)
			}
		})

	return s
}
