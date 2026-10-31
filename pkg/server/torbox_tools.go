package server

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/amxv/stremio-catalog-mcp/pkg/catalog"
	"github.com/amxv/stremio-catalog-mcp/pkg/torbox"
)

type TorboxUploadsGetInput struct {
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum number of items to return (1-100, default 20)"`
	Type        string `json:"type,omitempty" jsonschema:"Filter by upload type: all, torrents, or webdl (default all)"`
	Status      string `json:"status,omitempty" jsonschema:"Filter by status: all, completed, or active (default all)"`
	BypassCache bool   `json:"bypass_cache,omitempty" jsonschema:"Bypass TorBox cache to retrieve real-time progress"`
}

type TorboxUploadsGetResult struct {
	Items   []torbox.UploadItem `json:"items"`
	Count   int                 `json:"count"`
	Message string              `json:"message"`
}

func (rt Runtime) torboxUploadsGet(ctx context.Context, input TorboxUploadsGetInput) (TorboxUploadsGetResult, error) {
	if rt.TorboxAPIKey == "" {
		return TorboxUploadsGetResult{Message: "TORBOX_API_KEY is not configured in server environment variables."}, nil
	}

	client := torbox.NewClient(rt.TorboxAPIKey)
	if rt.TorboxBaseURL != "" {
		client.BaseURL = rt.TorboxBaseURL
	}
	uploads, err := client.GetUploads(ctx, input.BypassCache)
	if err != nil {
		return TorboxUploadsGetResult{}, fmt.Errorf("failed to retrieve TorBox uploads: %w", err)
	}

	var filtered []torbox.UploadItem
	for _, u := range uploads {
		if input.Type != "" && input.Type != "all" {
			if !strings.EqualFold(u.Type, input.Type) {
				continue
			}
		}
		if input.Status != "" && input.Status != "all" {
			isCompleted := strings.EqualFold(u.Status, "completed") || strings.EqualFold(u.Status, "cached")
			if strings.EqualFold(input.Status, "completed") && !isCompleted {
				continue
			}
			if strings.EqualFold(input.Status, "active") && isCompleted {
				continue
			}
		}
		filtered = append(filtered, u)
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 20
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	msg := fmt.Sprintf("Found %d TorBox upload(s).", len(filtered))
	return TorboxUploadsGetResult{
		Items:   filtered,
		Count:   len(filtered),
		Message: msg,
	}, nil
}

type TorboxCatalogSyncInput struct {
	Profile string `json:"profile,omitempty" jsonschema:"Profile name (e.g. kids, Suji, default)"`
	RowName string `json:"row_name,omitempty" jsonschema:"Display name for the catalog row (default 'TorBox Cloud')"`
	RowType string `json:"row_type,omitempty" jsonschema:"movie or series (default movie)"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Max completed uploads to resolve and sync (1-50, default 20)"`
	Mode    string `json:"mode,omitempty" jsonschema:"replace (default) to mirror current uploads, or append"`
}

type TorboxCatalogSyncResult struct {
	RowID        string                 `json:"row_id,omitempty"`
	RowName      string                 `json:"row_name,omitempty"`
	MatchedCount int                    `json:"matched_count"`
	TotalUploads int                    `json:"total_uploads"`
	Changes      []catalog.ChangeResult `json:"changes,omitempty"`
	Sync         CatalogSyncResult      `json:"sync"`
	Message      string                 `json:"message"`
}

var (
	uploadYearRegex   = regexp.MustCompile(`\b(19\d\d|20\d\d)\b`)
	uploadTagsRegex   = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|480p|4k|uhd|hdr|hdr10|dv|bluray|blu-ray|bdrip|brrip|web-dl|webrip|web|hdtv|dvdrip|remux|x264|x265|hevc|h264|h265|avc|aac|ac3|dts|atmos|ddp5\.1|dd5\.1|sub|dub|multi|tamil|telugu|hindi|malayalam|kannada|english|proper|repack)\b.*$`)
	uploadDelimRegex  = regexp.MustCompile(`[._]`)
	uploadCleanRegex  = regexp.MustCompile(`[\[\]\(\)\{\}]`)
	uploadSpacesRegex = regexp.MustCompile(`\s+`)
)

func cleanUploadTitle(raw string) (string, int) {
	if idx := strings.LastIndex(raw, "."); idx != -1 && len(raw)-idx <= 5 {
		raw = raw[:idx]
	}
	var year int
	if match := uploadYearRegex.FindString(raw); match != "" {
		if y, err := strconv.Atoi(match); err == nil && y >= 1900 && y <= time.Now().Year()+2 {
			year = y
		}
	}
	s := uploadDelimRegex.ReplaceAllString(raw, " ")
	if year > 0 {
		if idx := strings.Index(s, strconv.Itoa(year)); idx != -1 {
			s = s[:idx]
		}
	} else {
		if loc := uploadTagsRegex.FindStringIndex(s); loc != nil {
			s = s[:loc[0]]
		}
	}
	s = uploadCleanRegex.ReplaceAllString(s, " ")
	s = strings.TrimSpace(uploadSpacesRegex.ReplaceAllString(s, " "))
	return s, year
}

func (rt Runtime) torboxCatalogSync(ctx context.Context, input TorboxCatalogSyncInput) (TorboxCatalogSyncResult, error) {
	if rt.TorboxAPIKey == "" {
		return TorboxCatalogSyncResult{Message: "TORBOX_API_KEY is not configured in server environment variables."}, nil
	}
	if input.Profile != "" {
		rt = rt.forProfile(input.Profile)
	}

	rowType := input.RowType
	if rowType == "" {
		rowType = "movie"
	}
	if !catalog.ValidType(rowType) {
		return TorboxCatalogSyncResult{}, fmt.Errorf("invalid row_type %q (must be movie or series)", rowType)
	}

	rowName := strings.TrimSpace(input.RowName)
	if rowName == "" {
		rowName = "TorBox Cloud"
	}

	mode := input.Mode
	if mode == "" {
		mode = "replace"
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	client := torbox.NewClient(rt.TorboxAPIKey)
	if rt.TorboxBaseURL != "" {
		client.BaseURL = rt.TorboxBaseURL
	}
	uploads, err := client.GetUploads(ctx, true)
	if err != nil {
		return TorboxCatalogSyncResult{}, fmt.Errorf("failed to fetch TorBox uploads: %w", err)
	}

	var itemsToSync []catalog.ItemInput
	seen := make(map[string]bool)

	for _, u := range uploads {
		if len(itemsToSync) >= limit {
			break
		}
		// Only sync completed / cached releases
		if !strings.EqualFold(u.Status, "completed") && !strings.EqualFold(u.Status, "cached") {
			continue
		}

		cleanTitle, year := cleanUploadTitle(u.Name)
		if cleanTitle == "" {
			continue
		}

		// Try resolving against Cinemeta
		resolved, err := rt.Manager.Resolver.Resolve(ctx, rowType, catalog.ItemInput{Title: cleanTitle, Year: year})
		if err != nil || resolved.ID == "" {
			continue
		}

		if seen[resolved.ID] {
			continue
		}
		seen[resolved.ID] = true
		itemsToSync = append(itemsToSync, catalog.ItemInput{
			ID:    resolved.ID,
			Title: resolved.Title,
			Year:  resolved.Year,
		})
	}

	if len(itemsToSync) == 0 {
		return TorboxCatalogSyncResult{
			TotalUploads: len(uploads),
			MatchedCount: 0,
			Message:      fmt.Sprintf("Found %d TorBox upload(s), but none matched %s titles in Cinemeta. Use the dynamic 'TorBox Cloud' catalogue in Stremio for direct access.", len(uploads), rowType),
		}, nil
	}

	// Locate existing row with the same name if present
	state, err := rt.Manager.Read(ctx)
	var existingRowID string
	if err == nil {
		for _, r := range state.Rows {
			if strings.EqualFold(r.Name, rowName) && r.Type == rowType {
				existingRowID = r.ID
				break
			}
		}
	}

	batchInput := catalog.BatchInput{
		Profile: input.Profile,
		Upsert: []catalog.UpsertInput{
			{
				ID:    existingRowID,
				Name:  rowName,
				Type:  rowType,
				Items: itemsToSync,
				Mode:  mode,
			},
		},
	}

	updateRes, err := rt.catalogsUpdate(ctx, batchInput)
	if err != nil {
		return TorboxCatalogSyncResult{}, fmt.Errorf("failed to update catalog row: %w", err)
	}

	targetRowID := existingRowID
	if targetRowID == "" && len(updateRes.Changes) > 0 {
		targetRowID = updateRes.Changes[0].ID
	}

	return TorboxCatalogSyncResult{
		RowID:        targetRowID,
		RowName:      rowName,
		MatchedCount: len(itemsToSync),
		TotalUploads: len(uploads),
		Changes:      updateRes.Changes,
		Sync:         updateRes.Sync,
		Message:      fmt.Sprintf("Successfully synced %d TorBox upload(s) into row %q (%s).", len(itemsToSync), rowName, targetRowID),
	}, nil
}
