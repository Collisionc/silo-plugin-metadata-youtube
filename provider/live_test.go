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
// when YOUTUBE_API_KEY is set, and spends about 110 quota units:
//
//	YOUTUBE_API_KEY=... go test -run TestLive -v ./provider/
func TestLive(t *testing.T) {
	key := os.Getenv("YOUTUBE_API_KEY")
	if key == "" {
		t.Skip("YOUTUBE_API_KEY not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := New(youtube.NewClient("", key, nil), time.UTC)

	hits, err := p.Search(ctx, "Brain Break Land", nil)
	if err != nil {
		t.Fatalf("title search: %v", err)
	}
	if len(hits) == 0 || hits[0].ID != channelID {
		t.Fatalf("title search did not rank the channel first: %+v", hits)
	}
	t.Logf("search: %d hits, first %q (%s, %d)", len(hits), hits[0].Title, hits[0].ID, hits[0].Year)

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
