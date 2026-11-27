package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/collisionc/silo-plugin-metadata-youtube/youtube"
)

const (
	testKey     = "test-key"
	channelID   = "UCA94TZluH5eQtkQpaCZQfRw"
	uploadsID   = "UUA94TZluH5eQtkQpaCZQfRw"
	playlistID  = "PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat"
	bannerImage = "https://yt3.googleusercontent.com/banner"
	// underscoreChannelID has an underscore, which Silo turns into a space
	// before it searches.
	underscoreChannelID = "UC8WBLj_aquoUjJkXPXMC5aA"
)

// fakeYouTube serves canned API answers and counts calls per resource.
type fakeYouTube struct {
	mu    sync.Mutex
	calls map[string]int
	// quota makes every call answer 403 quotaExceeded.
	quota bool
	// noSearchHits makes a channel search find nothing.
	noSearchHits bool
	// gate, when set, holds playlistItems and videos calls until it closes.
	gate chan struct{}
	// gone are video IDs YouTube does not know, besides "deleted1234".
	gone map[string]bool
}

// holdListings makes playlistItems and videos calls wait until the returned
// function is called (or the test ends).
func (f *fakeYouTube) holdListings(t *testing.T) (release func()) {
	gate := make(chan struct{})
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	return release
}

func (f *fakeYouTube) count(resource string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[resource]
}

func (f *fakeYouTube) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resource := strings.TrimPrefix(r.URL.Path, "/")
	f.mu.Lock()
	f.calls[resource]++
	quota := f.quota
	gate := f.gate
	f.mu.Unlock()
	if gate != nil && (resource == "playlistItems" || resource == "videos") {
		<-gate
	}
	if r.URL.Query().Get("key") != testKey {
		http.Error(w, `{"error":{"code":400,"message":"API key not valid","errors":[{"reason":"badRequest"}]}}`, http.StatusBadRequest)
		return
	}
	if quota {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"The request cannot be completed because you have exceeded your quota.","errors":[{"reason":"quotaExceeded"}]}}`))
		return
	}
	q := r.URL.Query()
	var body any
	switch resource {
	case "channels":
		items := []any{}
		requested := "," + q.Get("id") + ","
		if strings.Contains(requested, ","+channelID+",") || q.Get("forHandle") == "@brainbreakland" {
			items = append(items, channelJSON())
		}
		if strings.Contains(requested, ","+underscoreChannelID+",") {
			ch := channelJSON()
			ch["id"] = underscoreChannelID
			ch["snippet"].(map[string]any)["title"] = "Matthew Wood (PE & Brain Breaks)"
			ch["contentDetails"] = map[string]any{"relatedPlaylists": map[string]any{"uploads": "UU8WBLj_aquoUjJkXPXMC5aA"}}
			items = append(items, ch)
		}
		body = map[string]any{"items": items}
	case "search":
		body = map[string]any{"items": []any{
			map[string]any{"id": map[string]any{"kind": "youtube#channel", "channelId": channelID}},
			map[string]any{"id": map[string]any{"kind": "youtube#channel", "channelId": "UCgone000000000000000000"}},
		}}
		f.mu.Lock()
		if f.noSearchHits {
			body = map[string]any{"items": []any{}}
		}
		f.mu.Unlock()
	case "playlists":
		if q.Get("id") == playlistID {
			body = map[string]any{"items": []any{map[string]any{
				"id": playlistID,
				"snippet": map[string]any{
					"title": "Fitness Runs", "description": "A playlist", "channelId": channelID,
					"channelTitle": "Brain Break Land", "publishedAt": "2022-01-01T00:00:00Z",
					"thumbnails": map[string]any{"high": map[string]any{"url": "https://i.ytimg.com/pl.jpg"}},
				},
			}}}
		} else {
			body = map[string]any{"items": []any{}}
		}
	case "playlistItems":
		body = playlistPage(q.Get("playlistId"), q.Get("pageToken"))
	case "videos":
		var items []any
		for _, id := range strings.Split(q.Get("id"), ",") {
			f.mu.Lock()
			gone := f.gone[id]
			f.mu.Unlock()
			if id == "deleted1234" || gone {
				continue
			}
			items = append(items, map[string]any{
				"id": id,
				"snippet": map[string]any{
					"title": "Full title of " + id, "description": "About " + id + "\n",
					"publishedAt": "2024-01-02T13:00:01Z",
					"channelId":   channelID, "channelTitle": "Brain Break Land",
					"thumbnails": map[string]any{
						"high":   map[string]any{"url": "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg", "width": 480, "height": 360},
						"maxres": map[string]any{"url": "https://i.ytimg.com/vi/" + id + "/maxresdefault.jpg", "width": 1280, "height": 720},
					},
				},
				"contentDetails": map[string]any{"duration": "PT5M14S"},
				"status":         map[string]any{"privacyStatus": "public"},
			})
		}
		body = map[string]any{"items": items}
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func channelJSON() map[string]any {
	return map[string]any{
		"id": channelID,
		"snippet": map[string]any{
			"title": "Brain Break Land", "description": "Fitness runs for kids.\n",
			"publishedAt": "2021-05-01T00:00:00Z",
			"thumbnails": map[string]any{
				"default": map[string]any{"url": "https://yt3.ggpht.com/avatar=s88"},
				"high":    map[string]any{"url": "https://yt3.ggpht.com/avatar=s800"},
			},
		},
		"contentDetails":   map[string]any{"relatedPlaylists": map[string]any{"uploads": uploadsID}},
		"brandingSettings": map[string]any{"image": map[string]any{"bannerExternalUrl": bannerImage}},
	}
}

func item(videoID, title, published, privacy string) map[string]any {
	return map[string]any{
		"snippet":        map[string]any{"title": title, "publishedAt": published},
		"contentDetails": map[string]any{"videoId": videoID, "videoPublishedAt": published},
		"status":         map[string]any{"privacyStatus": privacy},
	}
}

// playlistPage serves the uploads playlist over two pages, newest first as
// YouTube lists uploads, with a private and a deleted entry.
func playlistPage(id, token string) map[string]any {
	if id != uploadsID && id != playlistID {
		return map[string]any{"items": []any{}}
	}
	if token == "" {
		return map[string]any{
			"nextPageToken": "page2",
			"items": []any{
				item("newestvid01", "Newest", "2025-10-21T14:30:00Z", "public"),
				item("privatevid1", "Private video", "", "private"),
				item("lateday0102", "Second that day", "2024-01-02T20:00:00Z", "public"),
			},
		}
	}
	return map[string]any{"items": []any{
		item("0bNNs5GTru0", "Mario's Winter Run", "2024-01-02T13:00:01Z", "public"),
		item("deleted1234", "Deleted video", "2023-06-01T00:00:00Z", "public"),
		item("dec31video1", "New Year's Eve", "2023-12-31T09:00:00Z", "unlisted"),
	}}
}

func newTestProvider(t *testing.T) (*Provider, *fakeYouTube) {
	t.Helper()
	fake := &fakeYouTube{calls: map[string]int{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return New(youtube.NewClient(server.URL, testKey, server.Client()), time.UTC), fake
}

func TestSearchByFolderTagLooksTheChannelUp(t *testing.T) {
	p, fake := newTestProvider(t)
	hits, err := p.Search(context.Background(), "Brain Break Land [youtube-"+channelID+"]", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].ID != channelID || hits[0].Title != "Brain Break Land" || hits[0].Year != 2021 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[1].ID != uploadsID || hits[1].Title != "Uploads from Brain Break Land" || len(hits[1].Aliases) != 0 {
		t.Errorf("second hit = %+v, want the uploads playlist without aliases", hits[1])
	}
	if len(hits[0].Aliases) != 1 || hits[0].Aliases[0] != "Brain Break Land [youtube-"+channelID+"]" {
		t.Errorf("aliases = %q, want the folder title", hits[0].Aliases)
	}
	if hits[0].ImageURL != "https://yt3.ggpht.com/avatar=s800" {
		t.Errorf("poster = %q, want the largest avatar", hits[0].ImageURL)
	}
	if fake.count("search") != 0 {
		t.Error("an ID in the folder name must not spend 100 units on a search")
	}
}

func TestSearchByTitleKeepsYouTubesRanking(t *testing.T) {
	p, fake := newTestProvider(t)
	hits, err := p.Search(context.Background(), "Brain Break Land", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The second search result no longer exists and is dropped.
	if len(hits) != 1 || hits[0].ID != channelID {
		t.Fatalf("hits = %+v", hits)
	}
	if fake.count("search") != 1 || fake.count("channels") != 1 {
		t.Errorf("calls = %v", fake.calls)
	}
}

func TestSearchByHandleAndStoredID(t *testing.T) {
	p, _ := newTestProvider(t)
	hits, err := p.Search(context.Background(), "https://www.youtube.com/@brainbreakland", nil)
	if err != nil || len(hits) != 2 || hits[0].ID != channelID {
		t.Fatalf("handle: hits = %+v, err = %v", hits, err)
	}
	hits, err = p.Search(context.Background(), "anything", map[string]string{ProviderKey: playlistID})
	if err != nil || len(hits) != 1 || hits[0].ID != playlistID || hits[0].Title != "Fitness Runs" {
		t.Fatalf("stored ID: hits = %+v, err = %v", hits, err)
	}
}

// Folder names as Silo hands them over: tags it does not know are kept, but
// "_" becomes a space.
func TestSearchFolderTagsAsSiloPassesThem(t *testing.T) {
	p, fake := newTestProvider(t)
	ctx := context.Background()

	hits, err := p.Search(ctx, "BrainBreakLand [UCA94TZluH5eQtkQpaCZQfRw]", nil)
	if err != nil || len(hits) != 2 || hits[0].ID != channelID {
		t.Fatalf("bare tag: hits = %+v, err = %v", hits, err)
	}
	// Silo compares the folder title with the result's titles: the folder
	// title itself must be among them, with and without its tag.
	if strings.Join(hits[0].Aliases, "|") != "BrainBreakLand [UCA94TZluH5eQtkQpaCZQfRw]|BrainBreakLand" {
		t.Errorf("aliases = %q", hits[0].Aliases)
	}

	hits, err = p.Search(ctx, "Matthew Wood [UC8WBLj aquoUjJkXPXMC5aA]", nil)
	if err != nil || len(hits) != 2 || hits[0].ID != underscoreChannelID || hits[1].ID != "UU8WBLj_aquoUjJkXPXMC5aA" {
		t.Fatalf("underscore tag: hits = %+v, err = %v", hits, err)
	}
	if strings.Join(hits[0].Aliases, "|") != "Matthew Wood [UC8WBLj aquoUjJkXPXMC5aA]|Matthew Wood" {
		t.Errorf("aliases = %q", hits[0].Aliases)
	}
	if fake.count("search") != 0 {
		t.Error("an ID in the folder name must not spend 100 units on a search")
	}
	if fake.count("channels") != 2 {
		t.Errorf("channels calls = %d, want 2: the rebuilt candidates go in one lookup", fake.count("channels"))
	}
}

// A playlist folder ("BattleBots [PL...]") matches the playlist, carrying the
// folder title, with its owner's uploads as the weaker second candidate.
func TestSearchByPlaylistTag(t *testing.T) {
	p, fake := newTestProvider(t)
	ctx := context.Background()
	for _, query := range []string{
		"BattleBots [" + playlistID + "]",
		"BattleBots [PL22J3VaeABQD IZs7y60I3lUrrFTzkpat]",
	} {
		hits, err := p.Search(ctx, query, nil)
		if err != nil || len(hits) != 2 || hits[0].ID != playlistID || hits[1].ID != uploadsID {
			t.Fatalf("%q: hits = %+v, err = %v", query, hits, err)
		}
		if strings.Join(hits[0].Aliases, "|") != query+"|BattleBots" {
			t.Errorf("%q: aliases = %q", query, hits[0].Aliases)
		}
		if hits[1].Title != "Uploads from Brain Break Land" || len(hits[1].Aliases) != 0 {
			t.Errorf("%q: second hit = %+v", query, hits[1])
		}
	}
	if fake.count("search") != 0 || fake.count("channels") != 0 {
		t.Errorf("calls = %v, want playlist lookups only", fake.calls)
	}
}

// A folder named after the channel without its spaces still matches its
// title, but a channel search's other results get no alias.
func TestSearchByTitleRunTogether(t *testing.T) {
	p, _ := newTestProvider(t)
	hits, err := p.Search(context.Background(), "BrainBreakLand", nil)
	if err != nil || len(hits) != 1 || hits[0].ID != channelID {
		t.Fatalf("hits = %+v, err = %v", hits, err)
	}
	if len(hits[0].Aliases) != 1 || hits[0].Aliases[0] != "BrainBreakLand" {
		t.Errorf("aliases = %q", hits[0].Aliases)
	}
	hits, err = p.Search(context.Background(), "Brain Break Land", nil)
	if err != nil || len(hits) != 1 || len(hits[0].Aliases) != 0 {
		t.Fatalf("exact title: hits = %+v, err = %v", hits, err)
	}
}

// The uploads playlist offered beside a channel can be chosen and filled in.
func TestUploadsPlaylistSeries(t *testing.T) {
	p, fake := newTestProvider(t)
	detail, err := p.SeriesDetail(context.Background(), uploadsID)
	if err != nil || detail == nil {
		t.Fatalf("detail = %+v, err = %v", detail, err)
	}
	if detail.Title != "Uploads from Brain Break Land" || detail.PlaylistID != uploadsID || detail.PosterURL == "" || len(detail.Years) != 3 {
		t.Errorf("detail = %+v", detail.Series)
	}
	if fake.count("playlists") != 0 {
		t.Error("an uploads playlist is built from its channel, not looked up as a playlist")
	}
}

// Silo searches again on every scan of a folder it has not matched; each
// free-text search costs 100 units, so the answer - an empty one too - is kept
// for an hour.
func TestSearchByTitleIsAskedOnceAnHour(t *testing.T) {
	p, fake := newTestProvider(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }

	for range 3 {
		if hits, err := p.Search(ctx, "Brain Break Land", nil); err != nil || len(hits) != 1 {
			t.Fatalf("hits = %+v, err = %v", hits, err)
		}
	}
	// Same words, other case and extra spaces: the same question.
	if _, err := p.Search(ctx, "brain  break LAND", nil); err != nil {
		t.Fatal(err)
	}
	if fake.count("search") != 1 {
		t.Errorf("searches = %d, want 1", fake.count("search"))
	}
	// Nor are the channels it found looked up again, the one YouTube no
	// longer has among them: the folder's later scans cost nothing.
	if fake.count("channels") != 1 {
		t.Errorf("channel lookups = %d, want 1", fake.count("channels"))
	}

	fake.noSearchHits = true
	for range 3 {
		if hits, err := p.Search(ctx, "Nobody Here", nil); err != nil || len(hits) != 0 {
			t.Fatalf("hits = %+v, err = %v", hits, err)
		}
	}
	if fake.count("search") != 2 {
		t.Errorf("searches = %d, want 2: an empty answer is kept as well", fake.count("search"))
	}

	now = now.Add(searchTTL + time.Second)
	if _, err := p.Search(ctx, "Nobody Here", nil); err != nil {
		t.Fatal(err)
	}
	if fake.count("search") != 3 {
		t.Errorf("searches = %d, want 3: an hour later it is asked again", fake.count("search"))
	}
}

func TestSearchFailureIsNotKept(t *testing.T) {
	p, fake := newTestProvider(t)
	ctx := context.Background()
	fake.quota = true
	if _, err := p.Search(ctx, "Brain Break Land", nil); err == nil {
		t.Fatal("want the quota error")
	}
	fake.quota = false
	if hits, err := p.Search(ctx, "Brain Break Land", nil); err != nil || len(hits) != 1 {
		t.Fatalf("after the quota came back: hits = %+v, err = %v", hits, err)
	}
}

func TestCachesAreBounded(t *testing.T) {
	m := map[string]cached[int]{}
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	put(m, "old", 1, now, now.Add(-time.Minute), 3) // already expired
	put(m, "a", 1, now, now.Add(time.Hour), 3)
	put(m, "b", 1, now, now.Add(time.Hour), 3)
	put(m, "c", 1, now, now.Add(time.Hour), 3) // full: the expired entry makes room
	if _, ok := m["old"]; ok || len(m) != 3 {
		t.Fatalf("after dropping the expired entry: %v", m)
	}
	put(m, "a", 2, now, now.Add(time.Hour), 3) // replacing needs no room
	if len(m) != 3 || m["a"].value != 2 {
		t.Fatalf("after replacing: %v", m)
	}
	put(m, "d", 1, now, now.Add(time.Hour), 3) // full, nothing expired: start again
	if len(m) != 1 || m["d"].value != 1 {
		t.Fatalf("after starting again: %v", m)
	}
}

func TestSearchUnknownChannelFindsNothing(t *testing.T) {
	p, _ := newTestProvider(t)
	hits, err := p.Search(context.Background(), "Gone [UCgone000000000000000000]", nil)
	if err != nil || len(hits) != 0 {
		t.Fatalf("hits = %+v, err = %v", hits, err)
	}
}

func TestSeriesDetailSeasonsAndEpisodes(t *testing.T) {
	p, fake := newTestProvider(t)
	ctx := context.Background()

	detail, err := p.SeriesDetail(ctx, channelID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.FirstAirDate != "2023-06-01" || detail.LastAirDate != "2025-10-21" {
		t.Errorf("air dates = %s..%s", detail.FirstAirDate, detail.LastAirDate)
	}
	if got := detail.Years; len(got) != 3 || got[0] != 2023 || got[2] != 2025 {
		t.Errorf("years = %v", got)
	}
	if !strings.HasPrefix(detail.BannerURL, bannerImage+"=w2560") {
		t.Errorf("banner = %q, want the full-size banner", detail.BannerURL)
	}
	if detail.Overview != "Fitness runs for kids." {
		t.Errorf("overview = %q", detail.Overview)
	}

	seasons, err := p.Seasons(ctx, channelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 3 || seasons[1].Number != 2024 || seasons[1].Count != 2 || seasons[1].AirDate != "2024-01-02" {
		t.Errorf("seasons = %+v", seasons)
	}

	episodes, err := p.Episodes(ctx, channelID, 2024)
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 2 {
		t.Fatalf("episodes = %+v", episodes)
	}
	first := episodes[0]
	if first.VideoID != "0bNNs5GTru0" || first.Number != 10201 || first.AirDate != "2024-01-02" ||
		first.Title != "Full title of 0bNNs5GTru0" || first.Overview != "About 0bNNs5GTru0" ||
		first.RuntimeMinutes != 6 || first.StillURL != "https://i.ytimg.com/vi/0bNNs5GTru0/maxresdefault.jpg" {
		t.Errorf("first episode = %+v", first)
	}
	if episodes[1].VideoID != "lateday0102" || episodes[1].Number != 10202 {
		t.Errorf("second episode = %+v", episodes[1])
	}

	// A video that no longer has details keeps its listing title.
	old, err := p.Episodes(ctx, channelID, 2023)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 2 || old[0].VideoID != "deleted1234" || old[0].Title != "Deleted video" || old[0].Overview != "" {
		t.Errorf("2023 = %+v", old)
	}

	// One refresh lists the channel once, however many seasons it asks for.
	if n := fake.count("playlistItems"); n != 2 {
		t.Errorf("playlistItems calls = %d, want 2 (one listing of two pages)", n)
	}
	if n := fake.count("channels"); n != 1 {
		t.Errorf("channels calls = %d, want 1", n)
	}
	before := fake.count("videos")
	if _, err := p.Episodes(ctx, channelID, 2024); err != nil {
		t.Fatal(err)
	}
	if fake.count("videos") != before {
		t.Error("video details were fetched again inside their cache lifetime")
	}
}

func TestListingExpires(t *testing.T) {
	p, fake := newTestProvider(t)
	now := time.Now()
	p.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := p.Seasons(ctx, channelID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(listingTTL + time.Minute)
	if _, err := p.Seasons(ctx, channelID); err != nil {
		t.Fatal(err)
	}
	if n := fake.count("playlistItems"); n != 4 {
		t.Errorf("playlistItems calls = %d, want 4 (listed again after expiry)", n)
	}
}

func TestPlaylistSeries(t *testing.T) {
	p, _ := newTestProvider(t)
	detail, err := p.SeriesDetail(context.Background(), playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Kind != youtube.RefPlaylist || detail.ChannelName != "Brain Break Land" || detail.PlaylistID != playlistID {
		t.Errorf("detail = %+v", detail.Series)
	}
}

func TestQuotaExceededIsReportedNotRetried(t *testing.T) {
	p, fake := newTestProvider(t)
	fake.quota = true
	_, err := p.Search(context.Background(), "Brain Break Land", nil)
	var apiErr *youtube.APIError
	if !errors.As(err, &apiErr) || !apiErr.QuotaExceeded() {
		t.Fatalf("err = %v, want a quota error", err)
	}
	if fake.count("search") != 1 {
		t.Errorf("search calls = %d: a spent quota must not be retried", fake.count("search"))
	}
	if strings.Contains(err.Error(), testKey) {
		t.Error("the error message contains the API key")
	}
}

func TestNoAPIKey(t *testing.T) {
	p := New(youtube.NewClient("http://127.0.0.1:1", "", nil), nil)
	if _, err := p.Search(context.Background(), "Brain Break Land", nil); !errors.Is(err, youtube.ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
}

func TestSingleSeasonMode(t *testing.T) {
	fake := &fakeYouTube{calls: map[string]int{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	p := New(youtube.NewClient(server.URL, testKey, server.Client()), time.UTC, WithSeasonMode(SingleSeason))
	ctx := context.Background()

	seasons, err := p.Seasons(ctx, channelID)
	if err != nil || len(seasons) != 1 || seasons[0].Number != 1 || seasons[0].Count != 5 || seasons[0].AirDate != "2023-06-01" {
		t.Fatalf("seasons = %+v, err = %v", seasons, err)
	}
	episodes, err := p.Episodes(ctx, channelID, 1)
	if err != nil || len(episodes) != 5 {
		t.Fatalf("episodes = %+v, err = %v", episodes, err)
	}
	if episodes[2].VideoID != "0bNNs5GTru0" || episodes[2].Number != 2024010201 || episodes[2].AirDate != "2024-01-02" {
		t.Errorf("episode = %+v", episodes[2])
	}
	detail, err := p.SeriesDetail(ctx, channelID)
	if err != nil || detail.Seasons != 1 || len(detail.Years) != 3 || detail.Years[0] != 2023 {
		t.Errorf("detail seasons = %d, years = %v, err = %v", detail.Seasons, detail.Years, err)
	}
}

func TestBackdropChoices(t *testing.T) {
	cases := map[Backdrop]string{
		NoBackdrop:     "",
		BannerBackdrop: bannerImage + "=w2560",
		VideoBackdrop:  "https://i.ytimg.com/vi/newestvid01/maxresdefault.jpg",
	}
	for choice, want := range cases {
		fake := &fakeYouTube{calls: map[string]int{}}
		server := httptest.NewServer(fake)
		p := New(youtube.NewClient(server.URL, testKey, server.Client()), time.UTC, WithBackdrop(choice))
		detail, err := p.SeriesDetail(context.Background(), channelID)
		server.Close()
		if err != nil || detail.BackdropURL != want {
			t.Errorf("%s: backdrop = %q, want %q (err %v)", choice, detail.BackdropURL, want, err)
		}
	}
	if ParseBackdrop("") != NoBackdrop || ParseBackdrop("banner") != BannerBackdrop || ParseBackdrop("video") != VideoBackdrop || ParseBackdrop("x") != NoBackdrop {
		t.Error("ParseBackdrop")
	}
}

// shortCall is a context that gives up the way Silo's 30-second call
// deadline does, but at once.
func shortCall(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

// A channel too large to list within Silo's deadline is listed in the
// background: the call that ran out of time says so, the listing goes on, and
// the next call gets it without listing the channel again.
func TestSlowListingFinishesInTheBackground(t *testing.T) {
	p, fake := newTestProvider(t)
	p.work.margin = 0
	release := fake.holdListings(t)

	if _, err := p.Seasons(shortCall(t), channelID); !errors.Is(err, ErrStillWorking) {
		t.Fatalf("err = %v, want ErrStillWorking", err)
	}
	release()
	seasons, err := p.Seasons(context.Background(), channelID)
	if err != nil || len(seasons) != 3 {
		t.Fatalf("seasons = %+v, err = %v", seasons, err)
	}
	if got := fake.count("playlistItems"); got != 2 {
		t.Errorf("playlistItems calls = %d, want 2: the two pages, listed once", got)
	}
}

// Thousands of videos' details take longer than Silo waits too. The episodes
// then come back with the listing's titles and dates, enough to link the
// files, and the details fetched meanwhile fill in the next answer.
func TestSlowVideoDetailsLeaveTheListingsTitles(t *testing.T) {
	p, fake := newTestProvider(t)
	p.work.margin = 0
	if _, err := p.Seasons(context.Background(), channelID); err != nil {
		t.Fatal(err)
	}
	release := fake.holdListings(t)

	episodes, err := p.Episodes(shortCall(t), channelID, 2024)
	if err != nil || len(episodes) != 2 {
		t.Fatalf("episodes = %+v, err = %v", episodes, err)
	}
	if episodes[0].Title != "Mario's Winter Run" || episodes[0].AirDate != "2024-01-02" || episodes[0].RuntimeMinutes != 0 {
		t.Errorf("episode before its details = %+v, want the listing's title and date", episodes[0])
	}
	release()
	episodes, err = p.Episodes(context.Background(), channelID, 2024)
	if err != nil || len(episodes) != 2 || episodes[0].Title != "Full title of 0bNNs5GTru0" || episodes[0].RuntimeMinutes != 6 {
		t.Fatalf("episodes after the details = %+v, err = %v", episodes, err)
	}
	if got := fake.count("videos"); got != 1 {
		t.Errorf("videos calls = %d, want 1: the details fetched once", got)
	}
}
