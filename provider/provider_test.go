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
	f.mu.Unlock()
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
			items = append(items, ch)
		}
		body = map[string]any{"items": items}
	case "search":
		body = map[string]any{"items": []any{
			map[string]any{"id": map[string]any{"kind": "youtube#channel", "channelId": channelID}},
			map[string]any{"id": map[string]any{"kind": "youtube#channel", "channelId": "UCgone000000000000000000"}},
		}}
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
			if id == "deleted1234" {
				continue
			}
			items = append(items, map[string]any{
				"id": id,
				"snippet": map[string]any{
					"title": "Full title of " + id, "description": "About " + id + "\n",
					"publishedAt": "2024-01-02T13:00:01Z",
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
	if len(hits) != 1 || hits[0].ID != channelID || hits[0].Title != "Brain Break Land" || hits[0].Year != 2021 {
		t.Fatalf("hits = %+v", hits)
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
	if err != nil || len(hits) != 1 || hits[0].ID != channelID {
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
	if err != nil || len(hits) != 1 || hits[0].ID != channelID {
		t.Fatalf("bare tag: hits = %+v, err = %v", hits, err)
	}

	hits, err = p.Search(ctx, "Matthew Wood (PE & Brain Breaks) [UC8WBLj aquoUjJkXPXMC5aA]", nil)
	if err != nil || len(hits) != 1 || hits[0].ID != underscoreChannelID {
		t.Fatalf("underscore tag: hits = %+v, err = %v", hits, err)
	}
	if fake.count("search") != 0 {
		t.Error("an ID in the folder name must not spend 100 units on a search")
	}
	if fake.count("channels") != 2 {
		t.Errorf("channels calls = %d, want 2: the rebuilt candidates go in one lookup", fake.count("channels"))
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
