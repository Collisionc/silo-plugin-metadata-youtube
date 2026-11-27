package provider

import (
	"context"
	"strings"
	"time"

	"github.com/collisionc/silo-plugin-metadata-youtube/youtube"
)

// Movie is one video as a Silo movie: a video kept on its own in a movie
// library, not as an episode of its channel.
type Movie struct {
	VideoID     string
	Title       string
	Overview    string
	ChannelID   string
	ChannelName string
	// Year and ReleaseDate are the upload's, in the configured time zone.
	Year        int
	ReleaseDate string
	// RuntimeMinutes is rounded up, so a 30-second video still has a runtime.
	RuntimeMinutes int
	// Thumbnail is the video's largest thumbnail, the only art a video has.
	Thumbnail youtube.Thumbnail
}

// Movie looks a video up; nil when YouTube does not have it.
func (p *Provider) Movie(ctx context.Context, id string) (*Movie, error) {
	video, ok, err := p.Video(ctx, id)
	if err != nil || !ok {
		return nil, err
	}
	return p.movieFromVideo(video), nil
}

func (p *Provider) movieFromVideo(video youtube.Video) *Movie {
	movie := &Movie{
		VideoID:        video.ID,
		Title:          video.Snippet.Title,
		Overview:       strings.TrimSpace(video.Snippet.Description),
		ChannelID:      video.Snippet.ChannelID,
		ChannelName:    video.Snippet.ChannelTitle,
		RuntimeMinutes: RuntimeMinutes(video.ContentDetails.Duration),
		Thumbnail:      video.Snippet.Thumbnails.Best(),
	}
	if published := youtube.ParseTime(video.Snippet.PublishedAt); !published.IsZero() {
		movie.Year = published.In(p.loc).Year()
		movie.ReleaseDate = published.In(p.loc).Format(time.DateOnly)
	}
	return movie
}

// SearchMovies finds the video a movie's title (or an administrator's search)
// names: a YouTube ID already stored for the item, else the ID in a video URL
// or in a tag of the title, "Title [0bNNs5GTru0]" or "{youtube-0bNNs5GTru0}".
// There is no search by title: it would cost 100 quota units to guess at one
// video among billions. One call (1 unit) asks YouTube which of the IDs the
// title may hold exist, and the answer, a miss too, is kept for an hour:
// Silo searches again each time it scans a file it has not matched.
//
// Silo accepts a lone result automatically at a match score of 70. The exact
// title, here the title on disk returned as an alias, is worth 45, the
// provider 12 and having an ID 5; the missing points are the 20 for a
// matching year. So a name with the upload year, "Title [0bNNs5GTru0]
// (2024)", matches by itself (as does a year one or two off), and a name
// without one waits for Match.
func (p *Provider) SearchMovies(ctx context.Context, query string, providerIDs map[string]string) ([]SearchHit, error) {
	candidates := youtube.VideoIDCandidates(query)
	if id := strings.TrimSpace(providerIDs[ProviderKey]); youtube.IsVideoID(id) {
		candidates = []string{id}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	// A miss is kept among the searches, under a key no channel search has.
	missKey := "\x00video:" + strings.Join(candidates, ",")
	now := p.now()
	p.mu.Lock()
	miss, known := p.searches[missKey]
	p.mu.Unlock()
	if known && !now.After(miss.expires) {
		return nil, nil
	}
	details, err := p.videoDetails(ctx, candidates)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		video, ok := details[candidate]
		if !ok {
			continue
		}
		movie := p.movieFromVideo(video)
		return claimed([]SearchHit{{
			ID:       movie.VideoID,
			Title:    movie.Title,
			Year:     movie.Year,
			Overview: movie.Overview,
			ImageURL: movie.Thumbnail.URL,
			Aliases:  queryAliases(movie.Title, query),
		}}, providerIDs), nil
	}
	p.mu.Lock()
	put(p.searches, missKey, nil, now, now.Add(searchTTL), maxCachedSearches)
	p.mu.Unlock()
	return nil, nil
}

// MovieBackdrop returns a movie's background art as configured: the video's
// own thumbnail, its channel's banner (one API unit the first time, then
// cached), or none.
func (p *Provider) MovieBackdrop(ctx context.Context, movie *Movie) (string, error) {
	switch p.backdrop {
	case VideoBackdrop:
		return movie.Thumbnail.URL, nil
	case BannerBackdrop:
		if !youtube.IsChannelID(movie.ChannelID) {
			return "", nil
		}
		channel, err := p.SeriesByID(ctx, movie.ChannelID)
		if err != nil || channel == nil {
			return "", err
		}
		return channel.BannerURL, nil
	}
	return "", nil
}
