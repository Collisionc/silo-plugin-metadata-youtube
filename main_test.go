package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"google.golang.org/protobuf/types/known/structpb"
)

// The provider is a specialist: it must be seeded disabled so it never
// becomes the first provider of every new library. It serves movies, series,
// seasons and episodes: Silo offers a provider only the levels it gives a
// default priority.
func TestManifest(t *testing.T) {
	m, err := publicmanifest.Load(manifestJSON)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if m.PluginId != "collisionc.youtube" {
		t.Errorf("plugin_id = %q", m.PluginId)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0].Type != "metadata_provider.v1" || m.Capabilities[0].Id != "youtube" {
		t.Fatalf("capabilities = %+v", m.Capabilities)
	}
	meta := m.Capabilities[0].GetMetadata().AsMap()
	if enabled, ok := meta["default_enabled"].(bool); !ok || enabled {
		t.Errorf("default_enabled = %v, want false", meta["default_enabled"])
	}
	priority, _ := meta["default_priority"].(map[string]any)
	for _, level := range []string{"movie", "series", "season", "episode"} {
		if _, ok := priority[level]; !ok {
			t.Errorf("no default priority for %s", level)
		}
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(m.GetGlobalConfigSchema()[0].GetJsonSchema()), &schema); err != nil {
		t.Fatalf("config schema: %v", err)
	}
	for _, field := range m.GetGlobalConfigSchema()[0].GetAdminForm().GetFields() {
		if _, ok := schema.Properties[field.GetKey()]; !ok {
			t.Errorf("admin form field %q is not in the JSON schema", field.GetKey())
		}
		if strings.HasSuffix(field.GetKey(), "api_key") && !field.GetSecret() {
			t.Errorf("%s must be stored as a secret", field.GetKey())
		}
	}
}

// fakeAPICalls counts the requests the fake API has answered.
var fakeAPICalls atomic.Int64

func fakeAPI(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fakeAPICalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "channels":
			_, _ = w.Write([]byte(`{"items":[{"id":"UCA94TZluH5eQtkQpaCZQfRw","snippet":{"title":"Brain Break Land","description":"Runs","publishedAt":"2021-05-01T00:00:00Z","thumbnails":{"high":{"url":"https://yt3.ggpht.com/a=s800"}}},"contentDetails":{"relatedPlaylists":{"uploads":"UUA94TZluH5eQtkQpaCZQfRw"}},"brandingSettings":{"image":{"bannerExternalUrl":"https://yt3.googleusercontent.com/b"}}}]}`))
		case "playlistItems":
			_, _ = w.Write([]byte(`{"items":[{"snippet":{"title":"Run"},"contentDetails":{"videoId":"0bNNs5GTru0","videoPublishedAt":"2024-01-02T13:00:01Z"},"status":{"privacyStatus":"public"}}]}`))
		case "videos":
			if !strings.Contains(","+r.URL.Query().Get("id")+",", ",0bNNs5GTru0,") {
				_, _ = w.Write([]byte(`{"items":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"0bNNs5GTru0","snippet":{"title":"Mario's Winter Run","description":"How to play","publishedAt":"2024-01-02T13:00:01Z","channelId":"UCA94TZluH5eQtkQpaCZQfRw","channelTitle":"Brain Break Land","thumbnails":{"maxres":{"url":"https://i.ytimg.com/vi/0bNNs5GTru0/maxresdefault.jpg","width":1280,"height":720}}},"contentDetails":{"duration":"PT5M14S"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	previous := youtubeBaseURL
	youtubeBaseURL = server.URL
	t.Cleanup(func() { youtubeBaseURL = previous })
}

func configured(t *testing.T) *metadataServer {
	return configuredWith(t, map[string]any{"api_key": "k", "timezone": "Not/AZone", "backdrop": "banner"})
}

func configuredWith(t *testing.T, config map[string]any) *metadataServer {
	t.Helper()
	value, err := structpb.NewStruct(config)
	if err != nil {
		t.Fatal(err)
	}
	rs := &runtimeServer{}
	if err := rs.configure(context.Background(), []*pluginv1.ConfigEntry{{Key: configKey, Value: value}}); err != nil {
		t.Fatal(err)
	}
	return &metadataServer{runtime: rs}
}

func TestSeriesThroughTheGRPCHandlers(t *testing.T) {
	fakeAPI(t)
	s := configured(t)
	ctx := context.Background()

	search, err := s.Search(ctx, &pluginv1.SearchMetadataRequest{Query: "Brain Break Land [youtube-UCA94TZluH5eQtkQpaCZQfRw]", ItemType: "series"})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.GetResults()) != 2 || search.GetResults()[0].GetProviderId() != "UCA94TZluH5eQtkQpaCZQfRw" {
		t.Fatalf("search = %+v", search.GetResults())
	}
	if got := search.GetResults()[0].GetProviderIds().AsMap()["youtube"]; got != "UCA94TZluH5eQtkQpaCZQfRw" {
		t.Errorf("provider_ids.youtube = %v", got)
	}
	if aliases := search.GetResults()[0].GetTitleAliases(); len(aliases) != 1 || aliases[0].GetTitle() != "Brain Break Land [youtube-UCA94TZluH5eQtkQpaCZQfRw]" {
		t.Errorf("title_aliases = %+v, want the folder title", aliases)
	}

	book, err := s.Search(ctx, &pluginv1.SearchMetadataRequest{Query: "anything [0bNNs5GTru0]", ItemType: "book"})
	if err != nil || len(book.GetResults()) != 0 {
		t.Errorf("book search = %+v, %v; only series and movies are served", book.GetResults(), err)
	}

	ids, _ := structpb.NewStruct(map[string]any{"youtube": "UCA94TZluH5eQtkQpaCZQfRw"})
	meta, err := s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "series", ProviderIds: ids})
	if err != nil {
		t.Fatal(err)
	}
	item := meta.GetItem()
	if item.GetTitle() != "Brain Break Land" || item.GetYear() != 2024 || item.GetSeasonCount() != 1 ||
		item.GetFirstAirDate() != "2024-01-02" || item.GetNetworks()[0] != "YouTube" || item.GetBackdropPath() == "" {
		t.Errorf("series item = %+v", item)
	}

	seasons, err := s.GetSeasons(ctx, &pluginv1.GetSeasonsRequest{SeriesProviderId: "UCA94TZluH5eQtkQpaCZQfRw"})
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons.GetSeasons()) != 1 || seasons.GetSeasons()[0].GetSeasonNumber() != 2024 || seasons.GetSeasons()[0].GetProviderId() != "UCA94TZluH5eQtkQpaCZQfRw/2024" {
		t.Errorf("seasons = %+v", seasons.GetSeasons())
	}

	episodes, err := s.GetEpisodes(ctx, &pluginv1.GetEpisodesRequest{SeriesProviderId: "UCA94TZluH5eQtkQpaCZQfRw", SeasonNumber: 2024})
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes.GetEpisodes()) != 1 {
		t.Fatalf("episodes = %+v", episodes.GetEpisodes())
	}
	ep := episodes.GetEpisodes()[0]
	if ep.GetEpisodeNumber() != 10201 || ep.GetAirDate() != "2024-01-02" || ep.GetTitle() != "Mario's Winter Run" ||
		ep.GetRuntime() != 6 || ep.GetProviderId() != "0bNNs5GTru0" || ep.GetStillPath() == "" {
		t.Errorf("episode = %+v", ep)
	}

	images, err := s.GetImages(ctx, &pluginv1.GetImagesRequest{ItemType: "series", ProviderIds: ids})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, image := range images.GetImages() {
		kinds[image.GetKind()] = true
	}
	if !kinds["poster"] || !kinds["backdrop"] {
		t.Errorf("series images = %+v", images.GetImages())
	}
	seasonImages, err := s.GetImages(ctx, &pluginv1.GetImagesRequest{ItemType: "series", ProviderId: "UCA94TZluH5eQtkQpaCZQfRw/2024"})
	if err != nil || len(seasonImages.GetImages()) != 0 {
		t.Errorf("season images = %+v, %v", seasonImages.GetImages(), err)
	}
}

// Silo keeps no ID for an episode, so an episode lookup arrives with the
// series' ID or with none. The file's name says which video it is.
func TestEpisodeLookupFindsTheVideoInTheFileName(t *testing.T) {
	fakeAPI(t)
	s := configured(t)
	ctx := context.Background()
	series, _ := structpb.NewStruct(map[string]any{"youtube": "UCA94TZluH5eQtkQpaCZQfRw"})
	file := "/media/YouTube/Brain Break Land [UCA94TZluH5eQtkQpaCZQfRw]/2024-01-02 - Mario's Winter Run [0bNNs5GTru0].mkv"

	for name, req := range map[string]*pluginv1.GetMetadataRequest{
		"the series' ID and the file": {ItemType: "episode", ProviderIds: series, FilePath: file},
		"the file alone":              {ItemType: "episode", FilePath: file},
		"a curly tag":                 {ItemType: "episode", ProviderIds: series, FilePath: "/media/YouTube/Brain Break Land {yt-UCA94TZluH5eQtkQpaCZQfRw}/2024-01-02 - Mario's Winter Run {yt-0bNNs5GTru0}.mkv"},
		"a bare curly tag":            {ItemType: "episode", ProviderIds: series, FilePath: "/media/YouTube/Brain Break Land {UCA94TZluH5eQtkQpaCZQfRw}/2024-01-02 - Mario's Winter Run {0bNNs5GTru0}.mkv"},
		"the video's own ID":          {ItemType: "episode", ProviderId: "0bNNs5GTru0"},
	} {
		meta, err := s.GetMetadata(ctx, req)
		if err != nil || meta.GetItem().GetTitle() != "Mario's Winter Run" || meta.GetItem().GetProviderId() != "0bNNs5GTru0" {
			t.Errorf("%s: item = %+v, err = %v", name, meta.GetItem(), err)
		}
	}

	// Nothing names a video: no answer, and YouTube is not asked for one.
	before := fakeAPICalls.Load()
	meta, err := s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "episode", ProviderIds: series})
	if err != nil || meta.GetItem() != nil {
		t.Errorf("series ID alone: item = %+v, err = %v; want no answer", meta.GetItem(), err)
	}
	images, err := s.GetImages(ctx, &pluginv1.GetImagesRequest{ItemType: "episode", ProviderIds: series})
	if err != nil || len(images.GetImages()) != 0 {
		t.Errorf("series ID alone: images = %+v, err = %v; want none", images.GetImages(), err)
	}
	if asked := fakeAPICalls.Load() - before; asked != 0 {
		t.Errorf("%d API calls for a lookup that names no video", asked)
	}
}

// A single video in a movie library: the search finds it by the ID in the
// title Silo read from its file name, and the lookups by the ID Silo then
// keeps.
func TestMovieThroughTheGRPCHandlers(t *testing.T) {
	fakeAPI(t)
	s := configuredWith(t, map[string]any{"api_key": "k", "backdrop": "video"})
	ctx := context.Background()

	query := "Mario's Winter Run [0bNNs5GTru0]"
	search, err := s.Search(ctx, &pluginv1.SearchMetadataRequest{Query: query, ItemType: "movie", Year: 2024})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.GetResults()) != 1 {
		t.Fatalf("search = %+v", search.GetResults())
	}
	hit := search.GetResults()[0]
	if hit.GetProviderId() != "0bNNs5GTru0" || hit.GetItemType() != "movie" || hit.GetTitle() != "Mario's Winter Run" || hit.GetYear() != 2024 ||
		hit.GetProviderIds().AsMap()["youtube"] != "0bNNs5GTru0" || hit.GetImageUrl() == "" {
		t.Errorf("hit = %+v", hit)
	}
	if aliases := hit.GetTitleAliases(); len(aliases) != 1 || aliases[0].GetTitle() != query {
		t.Errorf("title_aliases = %+v, want the title on disk", aliases)
	}

	before := fakeAPICalls.Load()
	none, err := s.Search(ctx, &pluginv1.SearchMetadataRequest{Query: "Mario's Winter Run", ItemType: "movie"})
	if err != nil || len(none.GetResults()) != 0 {
		t.Errorf("search without an ID = %+v, %v; want nothing", none.GetResults(), err)
	}
	if asked := fakeAPICalls.Load() - before; asked != 0 {
		t.Errorf("%d API calls for a title that names no video", asked)
	}

	ids, _ := structpb.NewStruct(map[string]any{"youtube": "0bNNs5GTru0"})
	for name, req := range map[string]*pluginv1.GetMetadataRequest{
		"the stored ID": {ItemType: "movie", ProviderIds: ids},
		"the file":      {ItemType: "movie", FilePath: "/media/Videos/Mario's Winter Run [0bNNs5GTru0] (2024).mkv"},
	} {
		meta, err := s.GetMetadata(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		item := meta.GetItem()
		if item.GetProviderId() != "0bNNs5GTru0" || item.GetItemType() != "movie" || item.GetTitle() != "Mario's Winter Run" ||
			item.GetYear() != 2024 || item.GetReleaseDate() != "2024-01-02" || item.GetRuntime() != 6 || item.GetOverview() != "How to play" ||
			len(item.GetStudios()) != 1 || item.GetStudios()[0] != "Brain Break Land" ||
			item.GetPosterPath() == "" || item.GetBackdropPath() != item.GetPosterPath() {
			t.Errorf("%s: movie item = %+v", name, item)
		}
	}
	// A channel's ID is not a video's: no answer, and no quota spent.
	before = fakeAPICalls.Load()
	channel, _ := structpb.NewStruct(map[string]any{"youtube": "UCA94TZluH5eQtkQpaCZQfRw"})
	meta, err := s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "movie", ProviderIds: channel})
	if err != nil || meta.GetItem() != nil || fakeAPICalls.Load() != before {
		t.Errorf("channel ID as a movie: item = %+v, err = %v, calls = %d", meta.GetItem(), err, fakeAPICalls.Load()-before)
	}

	images, err := s.GetImages(ctx, &pluginv1.GetImagesRequest{ItemType: "movie", ProviderIds: ids})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, image := range images.GetImages() {
		kinds[image.GetKind()] = true
	}
	if len(images.GetImages()) != 2 || !kinds["poster"] || !kinds["backdrop"] {
		t.Errorf("movie images = %+v", images.GetImages())
	}

	// The default backdrop is none; the channel banner costs a channel lookup.
	s = configuredWith(t, map[string]any{"api_key": "k"})
	meta, err = s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "movie", ProviderIds: ids})
	if err != nil || meta.GetItem().GetBackdropPath() != "" || meta.GetItem().GetPosterPath() == "" {
		t.Errorf("default backdrop: item = %+v, err = %v", meta.GetItem(), err)
	}
	s = configuredWith(t, map[string]any{"api_key": "k", "backdrop": "banner"})
	meta, err = s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "movie", ProviderIds: ids})
	if err != nil || meta.GetItem().GetBackdropPath() != "https://yt3.googleusercontent.com/b=w2560" {
		t.Errorf("banner backdrop = %q, err = %v", meta.GetItem().GetBackdropPath(), err)
	}
}

// "Brain Break Land {imdb-tt4031218} {yt-UC...}": Silo takes the imdb tag out
// of the title and sends its ID with the search; the result the name
// identifies carries it. A refresh sends the series back with the YouTube ID
// alone, whatever IMDb ID the entry has: Silo keeps that itself.
func TestIMDbIDSiloSendsWithASearch(t *testing.T) {
	fakeAPI(t)
	ctx := context.Background()
	s := configuredWith(t, map[string]any{"api_key": "k"})

	sent, _ := structpb.NewStruct(map[string]any{"imdb": "tt4031218"})
	search, err := s.Search(ctx, &pluginv1.SearchMetadataRequest{Query: "Brain Break Land {yt-UCA94TZluH5eQtkQpaCZQfRw}", ItemType: "series", ProviderIds: sent})
	if err != nil || len(search.GetResults()) != 2 {
		t.Fatalf("search = %+v, err = %v", search.GetResults(), err)
	}
	if got := search.GetResults()[0].GetProviderIds().AsMap(); got["imdb"] != "tt4031218" || got["youtube"] != "UCA94TZluH5eQtkQpaCZQfRw" {
		t.Errorf("first result ids = %v, want the channel with the IMDb ID", got)
	}
	if got := search.GetResults()[1].GetProviderIds().AsMap(); got["imdb"] != nil {
		t.Errorf("second result ids = %v, want no IMDb ID", got)
	}

	for name, ids := range map[string]map[string]any{
		"an entry without an IMDb ID": {"youtube": "UCA94TZluH5eQtkQpaCZQfRw"},
		"an entry with one":           {"youtube": "UCA94TZluH5eQtkQpaCZQfRw", "imdb": "tt0000001"},
	} {
		stored, _ := structpb.NewStruct(ids)
		meta, err := s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "series", ProviderIds: stored})
		if err != nil {
			t.Fatal(err)
		}
		if got := meta.GetItem().GetProviderIds().AsMap(); len(got) != 1 || got["youtube"] != "UCA94TZluH5eQtkQpaCZQfRw" {
			t.Errorf("%s: ids = %v, want the YouTube ID alone", name, got)
		}
	}
}

func TestUnconfiguredPluginAsksForAKey(t *testing.T) {
	s := &metadataServer{runtime: &runtimeServer{}}
	_, err := s.Search(context.Background(), &pluginv1.SearchMetadataRequest{Query: "x", ItemType: "series"})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveImageURLsOnlyPassesYouTubeImages(t *testing.T) {
	s := &metadataServer{runtime: &runtimeServer{}}
	pass := []string{
		"https://i.ytimg.com/vi/0bNNs5GTru0/maxresdefault.jpg",
		"https://i9.ytimg.com/vi/0bNNs5GTru0/hqdefault.jpg",
		"https://yt3.ggpht.com/a=s800",
		"https://yt3.googleusercontent.com/b=w2560",
	}
	refuse := []string{
		"/etc/passwd",
		"http://i.ytimg.com/a.jpg",                 // not https
		"https://example.com/a.jpg",                // another host
		"https://i.ytimg.com.example.com/a.jpg",    // a look-alike
		"https://evilytimg.com/a.jpg",              // a suffix without the dot
		"https://i.ytimg.com:8443/a.jpg",           // another port
		"https://user@i.ytimg.com/a.jpg",           // credentials
		"https://169.254.169.254/latest/meta-data", // a cloud metadata address
	}
	got, err := s.ResolveImageURLs(context.Background(), &pluginv1.ResolveImageURLsRequest{Paths: append(append([]string{}, pass...), refuse...)})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range pass {
		if got.GetUrls()[path] != path {
			t.Errorf("%s: got %q, want it passed on", path, got.GetUrls()[path])
		}
	}
	for _, path := range refuse {
		if got.GetUrls()[path] != "" {
			t.Errorf("%s: got %q, want it refused", path, got.GetUrls()[path])
		}
	}
	one, err := s.ResolveImageURL(context.Background(), &pluginv1.ResolveImageURLRequest{Path: "https://example.com/a.jpg"})
	if err != nil || one.GetUrl() != "" {
		t.Errorf("single: %q, %v", one.GetUrl(), err)
	}
}

// By default a series has no backdrop; one-season mode puts every video in
// season 1.
func TestConfigDefaultsAndSingleSeason(t *testing.T) {
	fakeAPI(t)
	ctx := context.Background()
	ids, _ := structpb.NewStruct(map[string]any{"youtube": "UCA94TZluH5eQtkQpaCZQfRw"})

	s := configuredWith(t, map[string]any{"api_key": "k"})
	meta, err := s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "series", ProviderIds: ids})
	if err != nil || meta.GetItem().GetBackdropPath() != "" {
		t.Errorf("default backdrop = %q, err = %v; want none", meta.GetItem().GetBackdropPath(), err)
	}

	s = configuredWith(t, map[string]any{"api_key": "k", "seasons": "single"})
	meta, err = s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "series", ProviderIds: ids})
	if err != nil || meta.GetItem().GetSeasonCount() != 1 || meta.GetItem().GetYear() != 2024 {
		t.Errorf("single season item = %+v, err = %v", meta.GetItem(), err)
	}
	episodes, err := s.GetEpisodes(ctx, &pluginv1.GetEpisodesRequest{SeriesProviderId: "UCA94TZluH5eQtkQpaCZQfRw", SeasonNumber: 1})
	if err != nil || len(episodes.GetEpisodes()) != 1 || episodes.GetEpisodes()[0].GetEpisodeNumber() != 2024010201 || episodes.GetEpisodes()[0].GetSeasonNumber() != 1 {
		t.Errorf("single season episodes = %+v, err = %v", episodes.GetEpisodes(), err)
	}
}
