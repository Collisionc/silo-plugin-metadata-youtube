package provider

import (
	"context"
	"testing"
	"time"
)

// A video kept as a movie is found by the ID in its title, as Silo read it
// from the file's name.
func TestSearchMoviesByTheIDInTheName(t *testing.T) {
	p, fake := newTestProvider(t)
	ctx := context.Background()
	for _, query := range []string{
		"Mario's Winter Run [0bNNs5GTru0]",
		"[0bNNs5GTru0]", // "[0bNNs5GTru0] (2024) Mario's Winter Run 4K.mkv"
		"Mario's Winter Run {youtube-0bNNs5GTru0}",
		"Mario's Winter Run [yt-0bNNs5GTru0]",
		"[Compilation] Best Runs [0bNNs5GTru0]",
		"https://www.youtube.com/watch?v=0bNNs5GTru0",
	} {
		hits, err := p.SearchMovies(ctx, query, nil)
		if err != nil || len(hits) != 1 {
			t.Fatalf("%q: hits = %+v, err = %v", query, hits, err)
		}
		hit := hits[0]
		if hit.ID != "0bNNs5GTru0" || hit.Title != "Full title of 0bNNs5GTru0" || hit.Year != 2024 ||
			hit.Overview != "About 0bNNs5GTru0" || hit.ImageURL != "https://i.ytimg.com/vi/0bNNs5GTru0/maxresdefault.jpg" {
			t.Errorf("%q: hit = %+v", query, hit)
		}
		if len(hit.Aliases) == 0 || hit.Aliases[0] != query {
			t.Errorf("%q: aliases = %q, want the title on disk first", query, hit.Aliases)
		}
	}
	if fake.count("search") != 0 {
		t.Error("a movie is never searched for by title")
	}

	// The ID Silo stored for the item wins over the title.
	hits, err := p.SearchMovies(ctx, "anything [deleted1234]", map[string]string{ProviderKey: "newestvid01"})
	if err != nil || len(hits) != 1 || hits[0].ID != "newestvid01" {
		t.Errorf("stored ID: hits = %+v, err = %v", hits, err)
	}
	// A title without an ID, or with a channel's: nothing, and no call.
	before := fake.count("videos")
	for _, query := range []string{"Mario's Winter Run", "Brain Break Land [" + channelID + "]", "Part [2 of 3]"} {
		if hits, err := p.SearchMovies(ctx, query, map[string]string{ProviderKey: channelID}); err != nil || len(hits) != 0 {
			t.Errorf("%q: hits = %+v, err = %v; want none", query, hits, err)
		}
	}
	if fake.count("videos") != before {
		t.Errorf("%d calls for titles that name no video", fake.count("videos")-before)
	}
}

// Silo turns the underscores of an ID into spaces. One call asks which of
// the IDs the tag can have been exists.
func TestSearchMoviesRebuildsUnderscores(t *testing.T) {
	p, fake := newTestProvider(t)
	fake.gone = map[string]bool{"a_b-c_d__eF": true, "a_b-c__d_eF": true}
	hits, err := p.SearchMovies(context.Background(), "Some Talk [a b-c d eF]", nil)
	if err != nil || len(hits) != 1 || hits[0].ID != "a__b-c_d_eF" {
		t.Fatalf("hits = %+v, err = %v", hits, err)
	}
	if fake.count("videos") != 1 {
		t.Errorf("videos calls = %d, want 1", fake.count("videos"))
	}
}

// Silo searches again at every scan of a file it has not matched: a title
// whose tag is no video's ID is asked about once an hour.
func TestSearchMoviesKeepsAMissForAnHour(t *testing.T) {
	p, fake := newTestProvider(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	for range 3 {
		if hits, err := p.SearchMovies(ctx, "Old Clip [deleted1234]", nil); err != nil || len(hits) != 0 {
			t.Fatalf("hits = %+v, err = %v", hits, err)
		}
	}
	if fake.count("videos") != 1 {
		t.Errorf("videos calls = %d, want 1", fake.count("videos"))
	}
	now = now.Add(searchTTL + time.Second)
	if _, err := p.SearchMovies(ctx, "Old Clip [deleted1234]", nil); err != nil {
		t.Fatal(err)
	}
	if fake.count("videos") != 2 {
		t.Errorf("videos calls = %d, want 2: an hour later it is asked again", fake.count("videos"))
	}

	fake.mu.Lock()
	fake.quota = true
	fake.mu.Unlock()
	if _, err := p.SearchMovies(ctx, "Another [quotaVideo1]", nil); err == nil {
		t.Error("a failed lookup must be an error, not a miss")
	}
	fake.mu.Lock()
	fake.quota = false
	fake.mu.Unlock()
	if hits, err := p.SearchMovies(ctx, "Another [quotaVideo1]", nil); err != nil || len(hits) != 1 {
		t.Errorf("after the failure: hits = %+v, err = %v; a failure is not kept", hits, err)
	}
}

func TestMovieDatesInTheConfiguredZone(t *testing.T) {
	p, _ := newTestProvider(t)
	movie, err := p.Movie(context.Background(), "0bNNs5GTru0")
	if err != nil || movie == nil {
		t.Fatalf("movie = %+v, err = %v", movie, err)
	}
	if movie.Year != 2024 || movie.ReleaseDate != "2024-01-02" || movie.RuntimeMinutes != 6 ||
		movie.ChannelID != channelID || movie.ChannelName != "Brain Break Land" || movie.Thumbnail.Width != 1280 {
		t.Errorf("movie = %+v", movie)
	}
	// 2024-01-02T13:00:01Z is already January 3rd in Kiritimati (UTC+14).
	p.loc = time.FixedZone("UTC+14", 14*3600)
	if movie, _ = p.Movie(context.Background(), "0bNNs5GTru0"); movie.ReleaseDate != "2024-01-03" {
		t.Errorf("release date = %q, want it in the configured zone", movie.ReleaseDate)
	}
	if gone, err := p.Movie(context.Background(), "deleted1234"); err != nil || gone != nil {
		t.Errorf("a deleted video: movie = %+v, err = %v", gone, err)
	}
}

// A video's URL typed into Silo's Match for a series names its channel.
func TestSearchSeriesByAVideoURL(t *testing.T) {
	p, fake := newTestProvider(t)
	ctx := context.Background()
	for _, query := range []string{"https://www.youtube.com/watch?v=0bNNs5GTru0", "https://youtu.be/0bNNs5GTru0?t=10"} {
		hits, err := p.Search(ctx, query, nil)
		if err != nil || len(hits) != 2 || hits[0].ID != channelID || hits[1].ID != uploadsID {
			t.Fatalf("%q: hits = %+v, err = %v", query, hits, err)
		}
	}
	if fake.count("search") != 0 || fake.count("videos") != 1 {
		t.Errorf("calls = %v, want one video lookup and no search", fake.calls)
	}
	if hits, err := p.Search(ctx, "https://youtu.be/deleted1234", nil); err != nil || len(hits) != 0 || fake.count("search") != 0 {
		t.Errorf("a deleted video: hits = %+v, err = %v, searches = %d", hits, err, fake.count("search"))
	}
}
