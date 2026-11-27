package provider

import (
	"context"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/collisionc/silo-plugin-metadata-youtube/youtube"
)

// TestLive checks the plugin against the real YouTube Data API. It runs only
// when YOUTUBE_API_KEY is set, and spends about 115 quota units:
//
//	YOUTUBE_API_KEY=... go test -run TestLive -v ./provider/
func TestLive(t *testing.T) {
	key := os.Getenv("YOUTUBE_API_KEY")
	if key == "" {
		t.Skip("YOUTUBE_API_KEY not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	usage := &youtube.Usage{}
	client := youtube.NewClient("", key, nil).CountInto(usage)
	p := New(client, time.UTC)

	hits, err := p.Search(ctx, "Brain Break Land", nil)
	if err != nil {
		t.Fatalf("title search: %v", err)
	}
	if len(hits) == 0 || hits[0].ID != channelID {
		t.Fatalf("title search did not rank the channel first: %+v", hits)
	}
	t.Logf("search: %d hits, first %q (%s, %d)", len(hits), hits[0].Title, hits[0].ID, hits[0].Year)

	// A channel ID with an underscore, as Silo passes the folder name: the
	// underscore has become a space.
	hits, err = p.Search(ctx, "Matthew Wood (PE & Brain Breaks) [UC8WBLj aquoUjJkXPXMC5aA]", nil)
	// The channel first, carrying the folder title as an alias, then its
	// uploads playlist as the second candidate.
	if err != nil || len(hits) != 2 || hits[0].ID != underscoreChannelID || hits[1].ID != "UU8WBLj_aquoUjJkXPXMC5aA" {
		t.Fatalf("underscore tag: hits = %+v, err = %v", hits, err)
	}
	if len(hits[0].Aliases) == 0 || !strings.Contains(hits[0].Aliases[0], "[UC8WBLj aquoUjJkXPXMC5aA]") {
		t.Errorf("underscore tag: aliases = %q, want the folder title", hits[0].Aliases)
	}
	t.Logf("underscore tag: %q (%s), then %q", hits[0].Title, hits[0].ID, hits[1].Title)

	detail, err := p.SeriesDetail(ctx, channelID)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	t.Logf("series %q: seasons %v, %s..%s", detail.Title, detail.Years, detail.FirstAirDate, detail.LastAirDate)
	checkImage(t, "poster", detail.PosterURL, 400)
	checkImage(t, "banner", detail.BannerURL, 1920)

	episodes, err := p.Episodes(ctx, channelID, 2024)
	if err != nil {
		t.Fatalf("episodes: %v", err)
	}
	found := false
	for _, ep := range episodes {
		if ep.VideoID == "0bNNs5GTru0" {
			found = true
			t.Logf("S%dE%d %s %q %d min", ep.Season, ep.Number, ep.AirDate, ep.Title, ep.RuntimeMinutes)
			if ep.AirDate != "2024-01-02" || ep.RuntimeMinutes == 0 || ep.StillURL == "" {
				t.Errorf("episode = %+v", ep)
			}
			checkImage(t, "still", ep.StillURL, 480)
		}
	}
	if !found {
		t.Errorf("2024 has %d episodes but not 0bNNs5GTru0", len(episodes))
	}

	// The calls the above did not make, each with its own field selection:
	// the key check, a video as a movie, a handle and a playlist. YouTube
	// refuses a selection it cannot read with HTTP 400, so an error here is
	// the plugin's; a handle or playlist that no longer exists is not.
	if err := client.CheckKey(ctx); err != nil {
		t.Errorf("key check: %v", err)
	}
	movie, err := p.Movie(ctx, "0bNNs5GTru0")
	if err != nil || movie == nil || movie.Title == "" || movie.Year != 2024 || movie.Thumbnail.URL == "" || movie.ChannelID != channelID {
		t.Errorf("movie = %+v, err = %v", movie, err)
	}
	for _, query := range []string{"@brainbreakland", "Some Playlist [" + playlistID + "]"} {
		hits, err := p.Search(ctx, query, nil)
		if err != nil {
			t.Errorf("%q: %v", query, err)
			continue
		}
		t.Logf("%q: %d hits", query, len(hits))
	}

	// A listing an hour old is checked by its first page, one call, when
	// YouTube gives the playlist's size and the page is as it was.
	total, byCall := usage.Take()
	t.Logf("so far: %d quota units (%s)", total, byCall)
	p.now = func() time.Time { return time.Now().Add(listingTTL + time.Minute) }
	if _, err := p.Seasons(ctx, channelID); err != nil {
		t.Fatalf("seasons, an hour on: %v", err)
	}
	if total, byCall := usage.Take(); byCall != "playlistItems=1" {
		t.Errorf("checking the listing cost %d units (%s), want playlistItems=1", total, byCall)
	}
}

func checkImage(t *testing.T, name, url string, minWidth int) {
	t.Helper()
	if url == "" {
		t.Errorf("%s: no URL", name)
		return
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Errorf("%s: %v", name, err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(contentType, "image/") {
		t.Errorf("%s: HTTP %d %s", name, resp.StatusCode, contentType)
		return
	}
	cfg, format, err := image.DecodeConfig(strings.NewReader(string(body)))
	if err != nil {
		t.Logf("%s: %s, %d bytes (size not decoded: %v)", name, contentType, len(body), err)
		return
	}
	t.Logf("%s: %s %dx%d", name, format, cfg.Width, cfg.Height)
	if cfg.Width < minWidth {
		t.Errorf("%s is %d px wide, want at least %d", name, cfg.Width, minWidth)
	}
}
