// Command plugin is the YouTube metadata provider for Silo: YouTube channels
// and playlists as TV series, one season per year of uploads and one episode
// per video.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtimedefault"

	"github.com/collisionc/silo-plugin-metadata-youtube/provider"
	"github.com/collisionc/silo-plugin-metadata-youtube/youtube"
)

var version string

//go:embed manifest.json
var manifestJSON []byte

const configKey = "youtube"

// youtubeBaseURL is replaced by tests.
var youtubeBaseURL = youtube.DefaultBaseURL

type runtimeServer struct {
	runtimedefault.Server

	manifest *pluginv1.PluginManifest

	mu       sync.RWMutex
	provider *provider.Provider
}

func (s *runtimeServer) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

// Configure reads the API key and time zone. An unknown time zone falls back
// to UTC with a warning rather than failing the plugin.
func (s *runtimeServer) Configure(_ context.Context, req *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	apiKey, zone, seasons, backdrop := "", "", "", ""
	for _, entry := range req.GetConfig() {
		if entry.GetKey() != configKey || entry.GetValue() == nil {
			continue
		}
		values := entry.GetValue().AsMap()
		if v, ok := values["api_key"].(string); ok {
			apiKey = strings.TrimSpace(v)
		}
		if v, ok := values["timezone"].(string); ok {
			zone = strings.TrimSpace(v)
		}
		if v, ok := values["seasons"].(string); ok {
			seasons = strings.TrimSpace(v)
		}
		if v, ok := values["backdrop"].(string); ok {
			backdrop = strings.TrimSpace(v)
		}
	}
	loc := time.UTC
	if zone != "" {
		if loaded, err := time.LoadLocation(zone); err == nil {
			loc = loaded
		} else {
			slog.Warn("youtube: unknown time zone, using UTC", "timezone", zone, "error", err)
		}
	}
	p := provider.New(youtube.NewClient(youtubeBaseURL, apiKey, nil), loc,
		provider.WithSeasonMode(provider.ParseSeasonMode(seasons)),
		provider.WithBackdrop(provider.ParseBackdrop(backdrop)))
	s.mu.Lock()
	s.provider = p
	s.mu.Unlock()
	return &pluginv1.ConfigureResponse{}, nil
}

func (s *runtimeServer) current() (*provider.Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.provider == nil {
		return nil, youtube.ErrNoAPIKey
	}
	return s.provider, nil
}

type metadataServer struct {
	pluginv1.UnimplementedMetadataProviderServer
	runtime *runtimeServer
}

func (s *metadataServer) Search(ctx context.Context, req *pluginv1.SearchMetadataRequest) (*pluginv1.SearchMetadataResponse, error) {
	response := &pluginv1.SearchMetadataResponse{}
	if req.GetItemType() != "series" {
		return response, nil
	}
	p, err := s.runtime.current()
	if err != nil {
		return nil, err
	}
	hits, err := p.Search(ctx, req.GetQuery(), stringMap(req.GetProviderIds()))
	if err != nil {
		return nil, err
	}
	for _, hit := range hits {
		ids, err := idStruct(hit.ID)
		if err != nil {
			return nil, err
		}
		var aliases []*pluginv1.TitleAlias
		for _, alias := range hit.Aliases {
			aliases = append(aliases, &pluginv1.TitleAlias{Title: alias, Kind: "alternate"})
		}
		response.Results = append(response.Results, &pluginv1.ProviderSearchResult{
			ProviderId:   hit.ID,
			ItemType:     req.GetItemType(),
			Title:        hit.Title,
			Year:         int32(hit.Year),
			Overview:     hit.Overview,
			ProviderIds:  ids,
			ImageUrl:     hit.ImageURL,
			TitleAliases: aliases,
		})
	}
	return response, nil
}

func (s *metadataServer) GetMetadata(ctx context.Context, req *pluginv1.GetMetadataRequest) (*pluginv1.GetMetadataResponse, error) {
	id := providerID(req.GetProviderIds(), req.GetProviderId())
	if id == "" {
		return &pluginv1.GetMetadataResponse{}, nil
	}
	p, err := s.runtime.current()
	if err != nil {
		return nil, err
	}
	switch req.GetItemType() {
	case "series":
		detail, err := p.SeriesDetail(ctx, id)
		if err != nil || detail == nil {
			return &pluginv1.GetMetadataResponse{}, err
		}
		item, err := seriesItem(detail)
		if err != nil {
			return nil, err
		}
		return &pluginv1.GetMetadataResponse{Item: item}, nil
	case "episode":
		video, ok, err := p.Video(ctx, id)
		if err != nil || !ok {
			return &pluginv1.GetMetadataResponse{}, err
		}
		ids, err := idStruct(video.ID)
		if err != nil {
			return nil, err
		}
		return &pluginv1.GetMetadataResponse{Item: &pluginv1.MetadataItem{
			ProviderId:  video.ID,
			ItemType:    "episode",
			Title:       video.Snippet.Title,
			Overview:    strings.TrimSpace(video.Snippet.Description),
			Runtime:     int32(provider.RuntimeMinutes(video.ContentDetails.Duration)),
			ProviderIds: ids,
			PosterPath:  video.Snippet.Thumbnails.Best().URL,
			ReleaseDate: dateOnly(video.Snippet.PublishedAt),
		}}, nil
	}
	return &pluginv1.GetMetadataResponse{}, nil
}

func seriesItem(detail *provider.SeriesDetail) (*pluginv1.MetadataItem, error) {
	ids, err := idStruct(detail.ID)
	if err != nil {
		return nil, err
	}
	year := 0
	if len(detail.Years) > 0 {
		year = detail.Years[0]
	} else if !detail.Published.IsZero() {
		year = detail.Published.Year()
	}
	item := &pluginv1.MetadataItem{
		ProviderId:   detail.ID,
		ItemType:     "series",
		Title:        detail.Title,
		Year:         int32(year),
		Overview:     detail.Overview,
		Networks:     []string{"YouTube"},
		ProviderIds:  ids,
		PosterPath:   detail.PosterURL,
		BackdropPath: detail.BackdropURL,
		SeasonCount:  int32(detail.Seasons),
		FirstAirDate: detail.FirstAirDate,
		LastAirDate:  detail.LastAirDate,
		Status:       "Continuing",
	}
	if detail.Kind == youtube.RefPlaylist && detail.ChannelName != "" {
		item.Studios = []string{detail.ChannelName}
	}
	return item, nil
}

func (s *metadataServer) GetPersonDetail(context.Context, *pluginv1.GetPersonDetailRequest) (*pluginv1.GetPersonDetailResponse, error) {
	return &pluginv1.GetPersonDetailResponse{}, nil
}

func (s *metadataServer) GetSeasons(ctx context.Context, req *pluginv1.GetSeasonsRequest) (*pluginv1.GetSeasonsResponse, error) {
	id := providerID(req.GetProviderIds(), req.GetSeriesProviderId())
	response := &pluginv1.GetSeasonsResponse{}
	if id == "" {
		return response, nil
	}
	p, err := s.runtime.current()
	if err != nil {
		return nil, err
	}
	seasons, err := p.Seasons(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, season := range seasons {
		seasonID := seasonProviderID(id, season.Number)
		ids, err := idStruct(seasonID)
		if err != nil {
			return nil, err
		}
		response.Seasons = append(response.Seasons, &pluginv1.SeasonRecord{
			SeasonNumber: int32(season.Number),
			Title:        strconv.Itoa(season.Number),
			AirDate:      season.AirDate,
			ProviderId:   seasonID,
			ProviderIds:  ids,
		})
	}
	return response, nil
}

// seasonProviderID names one year of a series ("UC.../2024"). Silo stores it
// as the season's ID; it never comes back as a series or video lookup.
func seasonProviderID(seriesID string, season int) string {
	return seriesID + "/" + strconv.Itoa(season)
}

func (s *metadataServer) GetEpisodes(ctx context.Context, req *pluginv1.GetEpisodesRequest) (*pluginv1.GetEpisodesResponse, error) {
	id := providerID(req.GetProviderIds(), req.GetSeriesProviderId())
	response := &pluginv1.GetEpisodesResponse{}
	if id == "" {
		return response, nil
	}
	p, err := s.runtime.current()
	if err != nil {
		return nil, err
	}
	episodes, err := p.Episodes(ctx, id, int(req.GetSeasonNumber()))
	if err != nil {
		return nil, err
	}
	for _, episode := range episodes {
		ids, err := idStruct(episode.VideoID)
		if err != nil {
			return nil, err
		}
		response.Episodes = append(response.Episodes, &pluginv1.EpisodeRecord{
			SeasonNumber:  int32(episode.Season),
			EpisodeNumber: int32(episode.Number),
			Title:         episode.Title,
			Overview:      episode.Overview,
			AirDate:       episode.AirDate,
			Runtime:       int32(episode.RuntimeMinutes),
			StillPath:     episode.StillURL,
			ProviderId:    episode.VideoID,
			ProviderIds:   ids,
		})
	}
	return response, nil
}

func (s *metadataServer) GetImages(ctx context.Context, req *pluginv1.GetImagesRequest) (*pluginv1.GetImagesResponse, error) {
	id := providerID(req.GetProviderIds(), req.GetProviderId())
	response := &pluginv1.GetImagesResponse{}
	if id == "" || strings.Contains(id, "/") {
		return response, nil // no lookup, or a season: seasons have no art of their own
	}
	p, err := s.runtime.current()
	if err != nil {
		return nil, err
	}
	switch req.GetItemType() {
	case "series":
		series, err := p.SeriesByID(ctx, id)
		if err != nil || series == nil {
			return response, err
		}
		if req.SeasonNumber != nil {
			return response, nil
		}
		if series.PosterURL != "" {
			response.Images = append(response.Images, &pluginv1.ImageRecord{Kind: "poster", Url: series.PosterURL})
		}
		backdrop, err := p.Backdrop(ctx, series)
		if err != nil {
			return response, err
		}
		if backdrop != "" {
			response.Images = append(response.Images, &pluginv1.ImageRecord{Kind: "backdrop", Url: backdrop})
		}
	case "episode":
		video, ok, err := p.Video(ctx, id)
		if err != nil || !ok {
			return response, err
		}
		if thumb := video.Snippet.Thumbnails.Best(); thumb.URL != "" {
			response.Images = append(response.Images, &pluginv1.ImageRecord{Kind: "still", Url: thumb.URL, Width: int32(thumb.Width), Height: int32(thumb.Height)})
		}
	}
	return response, nil
}

// Image URLs are stored as the absolute YouTube image URLs they are; there is
// nothing to resolve.
func (s *metadataServer) ResolveImageURL(_ context.Context, req *pluginv1.ResolveImageURLRequest) (*pluginv1.ResolveImageURLResponse, error) {
	return &pluginv1.ResolveImageURLResponse{Url: absoluteOnly(req.GetPath())}, nil
}

func (s *metadataServer) ResolveImageURLs(_ context.Context, req *pluginv1.ResolveImageURLsRequest) (*pluginv1.ResolveImageURLsResponse, error) {
	urls := make(map[string]string, len(req.GetPaths()))
	for _, path := range req.GetPaths() {
		urls[path] = absoluteOnly(path)
	}
	return &pluginv1.ResolveImageURLsResponse{Urls: urls}, nil
}

func absoluteOnly(path string) string {
	if strings.HasPrefix(path, "https://") {
		return path
	}
	return ""
}

func providerID(ids *structpb.Struct, fallback string) string {
	if id := stringMap(ids)[provider.ProviderKey]; id != "" {
		return id
	}
	return strings.TrimSpace(fallback)
}

func stringMap(value *structpb.Struct) map[string]string {
	out := make(map[string]string)
	if value == nil {
		return out
	}
	for key, raw := range value.AsMap() {
		if text, ok := raw.(string); ok && text != "" {
			out[key] = text
		}
	}
	return out
}

func idStruct(id string) (*structpb.Struct, error) {
	if id == "" {
		return nil, nil
	}
	return structpb.NewStruct(map[string]any{provider.ProviderKey: id})
}

func dateOnly(rfc3339 string) string {
	t := youtube.ParseTime(rfc3339)
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.DateOnly)
}

func main() {
	manifest, err := loadManifest()
	if err != nil {
		panic(err)
	}
	rs := &runtimeServer{manifest: manifest}
	runtime.Serve(runtime.ServeConfig{
		Servers: runtime.CapabilityServers{
			Runtime:          rs,
			MetadataProvider: &metadataServer{runtime: rs},
		},
	})
}

func loadManifest() (*pluginv1.PluginManifest, error) {
	manifest, err := publicmanifest.Load(manifestJSON)
	if err != nil {
		return nil, fmt.Errorf("load embedded manifest: %w", err)
	}
	if version != "" {
		manifest.Version = version
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		return nil, fmt.Errorf("read executable %q: %w", executable, err)
	}
	sum := sha256.Sum256(data)
	manifest.Checksum = hex.EncodeToString(sum[:])
	return manifest, nil
}
