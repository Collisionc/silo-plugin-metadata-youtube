// Command plugin is the YouTube metadata provider for Silo: YouTube channels
// and playlists as TV series, one season per year of uploads and one episode
// per video, and single videos as movies.
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/collisionc/silo-plugin-metadata-youtube/provider"
	"github.com/collisionc/silo-plugin-metadata-youtube/youtube"
)

var version string

//go:embed manifest.json
var manifestJSON []byte

const configKey = "youtube"

// youtubeBaseURL is replaced by tests.
var youtubeBaseURL = youtube.DefaultBaseURL

// runtimeServer holds the provider built from the plugin's settings.
type runtimeServer struct {
	mu       sync.RWMutex
	provider *provider.Provider
}

// settings are the values of the plugin's "youtube" config entry.
type settings struct {
	apiKey, timezone, seasons, backdrop string
	// how far back uploads are listed (uploadsfrom.go)
	uploadsFromYear, uploadsFromByID string
}

func readSettings(entries []*pluginv1.ConfigEntry) settings {
	var out settings
	for _, entry := range entries {
		if entry.GetKey() != configKey || entry.GetValue() == nil {
			continue
		}
		values := entry.GetValue().AsMap()
		text := func(key string) string {
			switch v := values[key].(type) {
			case string:
				return strings.TrimSpace(v)
			case float64: // a number typed into a text field can arrive as one
				return strconv.FormatFloat(v, 'f', -1, 64)
			}
			return ""
		}
		out = settings{
			apiKey: text("api_key"), timezone: text("timezone"), seasons: text("seasons"), backdrop: text("backdrop"),
			uploadsFromYear: text("uploads_from"), uploadsFromByID: text("uploads_from_by_id"),
		}
	}
	return out
}

// location is the configured time zone; UTC when it is unset.
func (s settings) location() (*time.Location, error) {
	if s.timezone == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(s.timezone)
}

// configure builds the provider from the plugin's settings. An unknown time
// zone falls back to UTC with a warning rather than failing the plugin.
func (s *runtimeServer) configure(_ context.Context, entries []*pluginv1.ConfigEntry) error {
	config := readSettings(entries)
	loc, err := config.location()
	if err != nil {
		logger.Warn("youtube: unknown time zone, using UTC", "timezone", config.timezone, "error", err)
		loc = time.UTC
	}
	from, problems := config.uploadsFrom()
	for _, problem := range problems {
		logger.Warn("youtube: a setting for how far back uploads are listed is ignored: " + problem)
	}
	options := []provider.Option{
		provider.WithSeasonMode(provider.ParseSeasonMode(config.seasons)),
		provider.WithBackdrop(provider.ParseBackdrop(config.backdrop)),
		provider.WithUploadsFrom(from),
	}
	p := provider.New(youtube.NewClient(youtubeBaseURL, config.apiKey, nil), loc, options...)
	s.mu.Lock()
	s.provider = p
	s.mu.Unlock()
	return nil
}

// configurationChecks answers Check Connection on the plugin's settings page.
type configurationChecks struct {
	pluginv1.UnimplementedConfigurationChecksServer
}

// checkTimeout keeps the check inside the 20 seconds Silo gives it.
const checkTimeout = 15 * time.Second

// TestConfiguration checks unsaved settings: the time zone, and the API key
// with one call that costs 1 quota unit. A failed check says what to change.
func (configurationChecks) TestConfiguration(ctx context.Context, req *pluginv1.TestConfigurationRequest) (*pluginv1.TestConfigurationResponse, error) {
	config := readSettings(req.GetConfig())
	failed := func(message string) (*pluginv1.TestConfigurationResponse, error) {
		return &pluginv1.TestConfigurationResponse{Ok: false, Message: message}, nil
	}
	if config.apiKey == "" {
		return failed("Enter a YouTube Data API v3 key.")
	}
	if _, err := config.location(); err != nil {
		return failed(fmt.Sprintf("%q is not a time zone name. Use an IANA name such as America/New_York, or leave it empty for UTC.", config.timezone))
	}
	if _, problems := config.uploadsFrom(); len(problems) > 0 {
		return failed(strings.Join(problems, " "))
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	err := youtube.NewClient(youtubeBaseURL, config.apiKey, &http.Client{Timeout: checkTimeout}).CheckKey(ctx)
	if err == nil {
		return &pluginv1.TestConfigurationResponse{Ok: true}, nil
	}
	var apiErr *youtube.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.QuotaExceeded():
		return failed("YouTube accepts the key, but its daily quota is used up. It resets at midnight Pacific time.")
	case errors.As(err, &apiErr) && (apiErr.Detail == "SERVICE_DISABLED" || apiErr.Reason == "accessNotConfigured"):
		return failed("The YouTube Data API v3 is not enabled for the key's Google Cloud project. Enable it under APIs & Services > Library. " + apiErr.Message)
	case errors.As(err, &apiErr) && apiErr.KeyRejected():
		return failed("YouTube rejected the API key: " + apiErr.Message)
	}
	return failed("Could not check the key with YouTube: " + err.Error())
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

func (s *metadataServer) Search(ctx context.Context, req *pluginv1.SearchMetadataRequest) (response *pluginv1.SearchMetadataResponse, err error) {
	defer func() {
		outcome := outcomeOf(true, len(response.GetResults()), err)
		if err == nil && req.GetItemType() != "series" && req.GetItemType() != "movie" {
			outcome = notAnswered
		}
		query := "query:set"
		if strings.TrimSpace(req.GetQuery()) == "" {
			query = "query:none"
		}
		lookups.note("Search/"+req.GetItemType(), outcome, query+" ids:"+idShape(ownID(req.GetProviderIds())),
			"query", req.GetQuery(), "year", req.GetYear(), "provider_ids", idsText(req.GetProviderIds()), "answers", len(response.GetResults()))
		err = statusError(err)
	}()
	response = &pluginv1.SearchMetadataResponse{}
	if req.GetItemType() != "series" && req.GetItemType() != "movie" {
		return response, nil
	}
	p, err := s.runtime.current()
	if err != nil {
		return nil, err
	}
	var hits []provider.SearchHit
	if req.GetItemType() == "movie" {
		hits, err = p.SearchMovies(ctx, req.GetQuery(), stringMap(req.GetProviderIds()))
	} else {
		hits, err = p.Search(ctx, req.GetQuery(), stringMap(req.GetProviderIds()))
	}
	if err != nil {
		return nil, err
	}
	for _, hit := range hits {
		ids, err := idsStruct(hit.ID, hit.IMDbID)
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

func (s *metadataServer) GetMetadata(ctx context.Context, req *pluginv1.GetMetadataRequest) (response *pluginv1.GetMetadataResponse, err error) {
	itemType := req.GetItemType()
	id := providerID(req.GetProviderIds(), req.GetProviderId())
	if itemType == "episode" || itemType == "movie" {
		id = videoID(req.GetProviderIds(), req.GetProviderId(), req.GetFilePath())
	}
	defer func() {
		answers := 0
		if response.GetItem() != nil {
			answers = 1
		}
		outcome := outcomeOf(id != "", answers, err)
		switch {
		case err != nil:
		case itemType == "season":
			outcome = noSeasons // Silo counts it a failed refresh; GetSeasons is where seasons come from
		case itemType != "series" && itemType != "episode" && itemType != "movie":
			outcome = notAnswered
		case id == "" && itemType != "series":
			outcome = noVideoID
		}
		lookups.note("GetMetadata/"+itemType, outcome,
			"provider_id:"+idShape(req.GetProviderId())+" ids:"+idShape(ownID(req.GetProviderIds()))+" file:"+pathShape(req.GetFilePath()),
			"provider_id", req.GetProviderId(), "provider_ids", idsText(req.GetProviderIds()), "file_path", req.GetFilePath(), "language", req.GetLanguage())
		err = statusError(err)
	}()
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
	case "movie":
		movie, err := p.Movie(ctx, id)
		if err != nil || movie == nil {
			return &pluginv1.GetMetadataResponse{}, err
		}
		backdrop, err := p.MovieBackdrop(ctx, movie)
		if err != nil {
			return nil, err
		}
		item, err := movieItem(movie, backdrop)
		if err != nil {
			return nil, err
		}
		return &pluginv1.GetMetadataResponse{Item: item}, nil
	}
	return &pluginv1.GetMetadataResponse{}, nil
}

// movieItem is a video as a movie. The video's thumbnail is its poster: a
// video has no other art.
func movieItem(movie *provider.Movie, backdrop string) (*pluginv1.MetadataItem, error) {
	ids, err := idStruct(movie.VideoID)
	if err != nil {
		return nil, err
	}
	item := &pluginv1.MetadataItem{
		ProviderId:   movie.VideoID,
		ItemType:     "movie",
		Title:        movie.Title,
		Year:         int32(movie.Year),
		Overview:     movie.Overview,
		Runtime:      int32(movie.RuntimeMinutes),
		ProviderIds:  ids,
		PosterPath:   movie.Thumbnail.URL,
		BackdropPath: backdrop,
		ReleaseDate:  movie.ReleaseDate,
	}
	if movie.ChannelName != "" {
		item.Studios = []string{movie.ChannelName}
	}
	return item, nil
}

// videoID picks the video an episode or movie lookup is about. Silo keeps a
// provider's ID for a series or a movie but not for an episode, so an
// episode's lookup can arrive with the series' ID or with none; the file
// names its own video ("... [0bNNs5GTru0].mkv"). An ID that is not a video's
// is never sent to YouTube as one: that would spend a quota unit to find
// nothing. A lookup that names no video is logged by its caller (lookupLog),
// with what it carried.
func videoID(ids *structpb.Struct, fallback, filePath string) string {
	for _, candidate := range []string{ownID(ids), strings.TrimSpace(fallback)} {
		if youtube.IsVideoID(candidate) {
			return candidate
		}
	}
	if id, ok := youtube.VideoIDFromPath(filePath); ok {
		return id
	}
	return ""
}

// ownID is this provider's ID among a lookup's provider IDs.
func ownID(ids *structpb.Struct) string {
	return stringMap(ids)[provider.ProviderKey]
}

// seriesItem is a channel or playlist as a series. It carries the YouTube ID
// alone: an entry's IMDb ID comes from the folder's {imdb-tt...} tag or its
// tvshow.nfo, which Silo reads itself, and the entry keeps it.
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
	// Episode numbers such as S2024E10201 only order the videos; Silo's
	// clients show an episode's air date in their place for a series that
	// asks for it, as Sportarr's leagues do.
	item.Metadata = &structpb.Struct{Fields: map[string]*structpb.Value{
		"episode_labels": structpb.NewStringValue(episodeLabelsAirDate),
	}}
	return item, nil
}

// episodeLabelsAirDate asks Silo to label a series' episodes by air date.
const episodeLabelsAirDate = "air_date"

func (s *metadataServer) GetPersonDetail(context.Context, *pluginv1.GetPersonDetailRequest) (*pluginv1.GetPersonDetailResponse, error) {
	return &pluginv1.GetPersonDetailResponse{}, nil
}

func (s *metadataServer) GetSeasons(ctx context.Context, req *pluginv1.GetSeasonsRequest) (response *pluginv1.GetSeasonsResponse, err error) {
	id := providerID(req.GetProviderIds(), req.GetSeriesProviderId())
	defer func() {
		lookups.note("GetSeasons", outcomeOf(id != "", len(response.GetSeasons()), err),
			"series:"+idShape(req.GetSeriesProviderId())+" ids:"+idShape(ownID(req.GetProviderIds())),
			"series_provider_id", req.GetSeriesProviderId(), "provider_ids", idsText(req.GetProviderIds()), "answers", len(response.GetSeasons()))
		err = statusError(err)
	}()
	response = &pluginv1.GetSeasonsResponse{}
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

func (s *metadataServer) GetEpisodes(ctx context.Context, req *pluginv1.GetEpisodesRequest) (response *pluginv1.GetEpisodesResponse, err error) {
	id := providerID(req.GetProviderIds(), req.GetSeriesProviderId())
	defer func() {
		season := "season:set"
		if req.GetSeasonNumber() == 0 {
			season = "season:zero"
		}
		lookups.note("GetEpisodes", outcomeOf(id != "", len(response.GetEpisodes()), err),
			"series:"+idShape(req.GetSeriesProviderId())+" ids:"+idShape(ownID(req.GetProviderIds()))+" "+season,
			"series_provider_id", req.GetSeriesProviderId(), "provider_ids", idsText(req.GetProviderIds()),
			"season_number", req.GetSeasonNumber(), "answers", len(response.GetEpisodes()))
		err = statusError(err)
	}()
	response = &pluginv1.GetEpisodesResponse{}
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

func (s *metadataServer) GetImages(ctx context.Context, req *pluginv1.GetImagesRequest) (response *pluginv1.GetImagesResponse, err error) {
	id := providerID(req.GetProviderIds(), req.GetProviderId())
	defer func() {
		outcome := outcomeOf(id != "", len(response.GetImages()), err)
		season := "season:unset"
		if req.SeasonNumber != nil {
			season = "season:set"
		}
		switch {
		case err != nil || id == "":
		case strings.Contains(id, "/"), req.GetItemType() == "series" && req.SeasonNumber != nil:
			outcome = notAnswered // seasons have no art of their own
		case (req.GetItemType() == "episode" || req.GetItemType() == "movie") && !youtube.IsVideoID(id):
			outcome = noVideoID
		case req.GetItemType() != "series" && req.GetItemType() != "episode" && req.GetItemType() != "movie":
			outcome = notAnswered
		}
		lookups.note("GetImages/"+req.GetItemType(), outcome,
			"provider_id:"+idShape(req.GetProviderId())+" ids:"+idShape(ownID(req.GetProviderIds()))+" "+season,
			"provider_id", req.GetProviderId(), "provider_ids", idsText(req.GetProviderIds()), "answers", len(response.GetImages()))
		err = statusError(err)
	}()
	response = &pluginv1.GetImagesResponse{}
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
		if !youtube.IsVideoID(id) {
			return response, nil // the series' ID: no video to look up
		}
		video, ok, err := p.Video(ctx, id)
		if err != nil || !ok {
			return response, err
		}
		if thumb := video.Snippet.Thumbnails.Best(); thumb.URL != "" {
			response.Images = append(response.Images, &pluginv1.ImageRecord{Kind: "still", Url: thumb.URL, Width: int32(thumb.Width), Height: int32(thumb.Height)})
		}
	case "movie":
		if !youtube.IsVideoID(id) {
			return response, nil
		}
		movie, err := p.Movie(ctx, id)
		if err != nil || movie == nil {
			return response, err
		}
		if thumb := movie.Thumbnail; thumb.URL != "" {
			response.Images = append(response.Images, &pluginv1.ImageRecord{Kind: "poster", Url: thumb.URL, Width: int32(thumb.Width), Height: int32(thumb.Height)})
		}
		backdrop, err := p.MovieBackdrop(ctx, movie)
		if err != nil {
			return response, err
		}
		if backdrop != "" {
			response.Images = append(response.Images, &pluginv1.ImageRecord{Kind: "backdrop", Url: backdrop})
		}
	}
	return response, nil
}

// Image URLs are stored as the absolute YouTube image URLs they are; there is
// nothing to resolve.
func (s *metadataServer) ResolveImageURL(_ context.Context, req *pluginv1.ResolveImageURLRequest) (*pluginv1.ResolveImageURLResponse, error) {
	return &pluginv1.ResolveImageURLResponse{Url: youtubeImageURL(req.GetPath())}, nil
}

func (s *metadataServer) ResolveImageURLs(_ context.Context, req *pluginv1.ResolveImageURLsRequest) (*pluginv1.ResolveImageURLsResponse, error) {
	urls := make(map[string]string, len(req.GetPaths()))
	for _, path := range req.GetPaths() {
		urls[path] = youtubeImageURL(path)
	}
	return &pluginv1.ResolveImageURLsResponse{Urls: urls}, nil
}

// imageHostSuffixes are where the YouTube Data API's images live: video
// thumbnails on i.ytimg.com (i9.ytimg.com for some), channel avatars on
// yt3.ggpht.com, banners on yt3.googleusercontent.com.
var imageHostSuffixes = []string{".ytimg.com", ".ggpht.com", ".googleusercontent.com"}

// youtubeImageURL passes on an https URL of a YouTube image host, and nothing
// else: every image this plugin gives Silo is one, so another URL would only
// point Silo's server somewhere this plugin never sent it.
func youtubeImageURL(path string) string {
	u, err := url.Parse(path)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range imageHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return path
		}
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

// idsStruct is idStruct with the item's IMDb title ID beside its YouTube ID,
// when it has one. Silo keeps both for the item.
func idsStruct(id, imdb string) (*structpb.Struct, error) {
	if id == "" || imdb == "" {
		return idStruct(id)
	}
	return structpb.NewStruct(map[string]any{provider.ProviderKey: id, provider.IMDbKey: imdb})
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

// The SDK loads the embedded manifest, stamps the version and the binary's
// checksum, answers the Runtime service (Configure through rs.configure) and
// registers the settings check the manifest declares.
func main() {
	rs := &runtimeServer{}
	// One line per start: it shows in Silo's log that this build runs and
	// that the plug-in's lines reach that log.
	logger.Info("youtube: started", "version", startedVersion())
	runtime.ServeManifestWithOptions(manifestJSON, version,
		runtime.CapabilityServers{MetadataProvider: &metadataServer{runtime: rs}},
		runtime.WithConfigure(rs.configure),
		runtime.WithConfigurationChecks(configurationChecks{}))
}

// startedVersion is the version this build reports: the one stamped at build
// time, else the manifest's.
func startedVersion() string {
	if version != "" {
		return version
	}
	if m, err := publicmanifest.Load(manifestJSON); err == nil {
		return m.GetVersion()
	}
	return ""
}
