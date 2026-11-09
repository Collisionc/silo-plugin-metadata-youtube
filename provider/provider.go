// Package provider turns YouTube channels and playlists into Silo series:
// the channel (or playlist) is the series, each year with uploads is a
// season, and each video an episode dated by its upload.
package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/collisionc/silo-plugin-metadata-youtube/youtube"
)

// ProviderKey is the provider-ID key this plugin stores (its capability ID).
const ProviderKey = "youtube"

const (
	listingTTL = time.Hour
	detailTTL  = 24 * time.Hour
	seriesTTL  = 6 * time.Hour
	// searchLimit is how many channels a free-text search offers. The search
	// itself costs 100 quota units whatever the count.
	searchLimit = 5
)

// Provider answers Silo's metadata calls from the YouTube Data API, with an
// in-memory cache so one library refresh does not list a channel once per
// season.
type Provider struct {
	yt  *youtube.Client
	loc *time.Location
	now func() time.Time

	mu       sync.Mutex
	series   map[string]cached[*Series]
	listings map[string]cached[[]NumberedUpload]
	videos   map[string]cached[youtube.Video]
}

type cached[T any] struct {
	value   T
	expires time.Time
}

// New returns a provider using client, dating uploads in loc (UTC when nil).
func New(client *youtube.Client, loc *time.Location) *Provider {
	if loc == nil {
		loc = time.UTC
	}
	return &Provider{
		yt:       client,
		loc:      loc,
		now:      time.Now,
		series:   make(map[string]cached[*Series]),
		listings: make(map[string]cached[[]NumberedUpload]),
		videos:   make(map[string]cached[youtube.Video]),
	}
}

// Series is a channel or playlist as a Silo series.
type Series struct {
	ID          string
	Kind        youtube.RefKind // RefChannel or RefPlaylist
	Title       string
	Overview    string
	ChannelName string
	PosterURL   string
	BannerURL   string
	Published   time.Time
	// PlaylistID lists the series' videos: a channel's uploads playlist, or
	// the playlist itself.
	PlaylistID string
}

// SearchHit is one candidate series.
type SearchHit struct {
	ID       string
	Title    string
	Year     int
	Overview string
	ImageURL string
}

// Search finds the series a library folder (or an administrator's search)
// names. In order: a YouTube ID already stored for the item, a playlist or
// channel ID or @handle in the text (a [youtube-UC...] folder tag, a pasted
// URL), and finally a YouTube channel search for the text without its
// bracketed tags.
func (p *Provider) Search(ctx context.Context, query string, providerIDs map[string]string) ([]SearchHit, error) {
	if id := strings.TrimSpace(providerIDs[ProviderKey]); id != "" {
		series, err := p.SeriesByID(ctx, id)
		if err != nil || series == nil {
			return nil, err
		}
		return []SearchHit{series.hit()}, nil
	}

	if ref, ok := youtube.ParseRef(query); ok {
		var (
			series *Series
			err    error
		)
		switch ref.Kind {
		case youtube.RefHandle:
			series, err = p.seriesByHandle(ctx, ref.ID)
		default:
			series, err = p.SeriesByID(ctx, ref.ID)
		}
		if err != nil || series == nil {
			return nil, err
		}
		return []SearchHit{series.hit()}, nil
	}

	text := youtube.CleanQuery(query)
	if text == "" {
		return nil, nil
	}
	ids, err := p.yt.SearchChannels(ctx, text, searchLimit)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	channels, err := p.yt.Channels(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]youtube.Channel, len(channels))
	for _, ch := range channels {
		byID[ch.ID] = ch
	}
	hits := make([]SearchHit, 0, len(ids))
	for _, id := range ids { // keep YouTube's ranking
		ch, ok := byID[id]
		if !ok {
			continue
		}
		series := seriesFromChannel(ch)
		p.storeSeries(series)
		hits = append(hits, series.hit())
	}
	return hits, nil
}

func (s *Series) hit() SearchHit {
	year := 0
	if !s.Published.IsZero() {
		year = s.Published.Year()
	}
	return SearchHit{ID: s.ID, Title: s.Title, Year: year, Overview: s.Overview, ImageURL: s.PosterURL}
}

// SeriesByID looks a channel (UC...) or playlist up; nil when YouTube does
// not know it.
func (p *Provider) SeriesByID(ctx context.Context, id string) (*Series, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	if series, ok := p.cachedSeries(id); ok {
		return series, nil
	}
	if youtube.IsChannelID(id) {
		channels, err := p.yt.Channels(ctx, []string{id})
		if err != nil || len(channels) == 0 {
			return nil, err
		}
		series := seriesFromChannel(channels[0])
		p.storeSeries(series)
		return series, nil
	}
	playlists, err := p.yt.Playlists(ctx, []string{id})
	if err != nil || len(playlists) == 0 {
		return nil, err
	}
	series := seriesFromPlaylist(playlists[0])
	p.storeSeries(series)
	return series, nil
}

func (p *Provider) seriesByHandle(ctx context.Context, handle string) (*Series, error) {
	ch, err := p.yt.ChannelByHandle(ctx, handle)
	if err != nil || ch == nil {
		return nil, err
	}
	series := seriesFromChannel(*ch)
	p.storeSeries(series)
	return series, nil
}

func seriesFromChannel(ch youtube.Channel) *Series {
	return &Series{
		ID:          ch.ID,
		Kind:        youtube.RefChannel,
		Title:       ch.Snippet.Title,
		Overview:    strings.TrimSpace(ch.Snippet.Description),
		ChannelName: ch.Snippet.Title,
		PosterURL:   channelAvatar(ch.Snippet.Thumbnails),
		BannerURL:   channelBanner(ch.BrandingSettings.Image.BannerExternalURL),
		Published:   youtube.ParseTime(ch.Snippet.PublishedAt),
		PlaylistID:  ch.UploadsPlaylistID(),
	}
}

func seriesFromPlaylist(pl youtube.Playlist) *Series {
	return &Series{
		ID:          pl.ID,
		Kind:        youtube.RefPlaylist,
		Title:       pl.Snippet.Title,
		Overview:    strings.TrimSpace(pl.Snippet.Description),
		ChannelName: pl.Snippet.ChannelTitle,
		PosterURL:   pl.Snippet.Thumbnails.Best().URL,
		Published:   youtube.ParseTime(pl.Snippet.PublishedAt),
		PlaylistID:  pl.ID,
	}
}

// channelAvatar is the channel's square avatar at its largest size (800 px,
// under "high"), used as the series poster.
func channelAvatar(thumbs youtube.Thumbnails) string {
	return thumbs.Best().URL
}

// channelBanner asks for the uncropped 2560x1440 banner. Without a size
// suffix the image server returns a small default.
func channelBanner(url string) string {
	if url == "" || strings.Contains(url, "=") {
		return url
	}
	return url + "=w2560-fcrop64=1,00000000ffffffff-k-c0xffffffff-no-nfpr-rj"
}

func (p *Provider) cachedSeries(id string) (*Series, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.series[id]
	if !ok || p.now().After(entry.expires) {
		return nil, false
	}
	return entry.value, true
}

func (p *Provider) storeSeries(series *Series) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.series[series.ID] = cached[*Series]{value: series, expires: p.now().Add(seriesTTL)}
}

// Listing returns a series' videos, numbered by Number.
func (p *Provider) Listing(ctx context.Context, series *Series) ([]NumberedUpload, error) {
	p.mu.Lock()
	entry, ok := p.listings[series.ID]
	p.mu.Unlock()
	if ok && !p.now().After(entry.expires) {
		return entry.value, nil
	}
	if series.PlaylistID == "" {
		return nil, fmt.Errorf("youtube: %s has no playlist to list", series.ID)
	}
	items, err := p.yt.PlaylistItems(ctx, series.PlaylistID)
	if err != nil {
		var apiErr *youtube.APIError
		// A channel with no public uploads has no uploads playlist.
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			items = nil
		} else {
			return nil, err
		}
	}
	uploads := make([]Upload, 0, len(items))
	for _, item := range items {
		if !listable(item) {
			continue
		}
		uploads = append(uploads, Upload{
			VideoID:   item.ContentDetails.VideoID,
			Title:     item.Snippet.Title,
			Published: youtube.ParseTime(item.ContentDetails.VideoPublishedAt),
		})
	}
	numbered := Number(uploads, p.loc)
	p.mu.Lock()
	p.listings[series.ID] = cached[[]NumberedUpload]{value: numbered, expires: p.now().Add(listingTTL)}
	p.mu.Unlock()
	return numbered, nil
}

// listable drops entries for videos that are private or deleted: they keep a
// place in a playlist but have no publish date or details.
func listable(item youtube.PlaylistItem) bool {
	switch item.Status.PrivacyStatus {
	case "private", "privacyStatusUnspecified":
		return false
	}
	return item.ContentDetails.VideoID != "" && item.ContentDetails.VideoPublishedAt != ""
}

// SeriesDetail is a series with the span of its uploads.
type SeriesDetail struct {
	*Series
	Years        []int
	FirstAirDate string
	LastAirDate  string
}

// SeriesDetail looks the series up and lists its videos for its air dates and
// season count.
func (p *Provider) SeriesDetail(ctx context.Context, id string) (*SeriesDetail, error) {
	series, err := p.SeriesByID(ctx, id)
	if err != nil || series == nil {
		return nil, err
	}
	listing, err := p.Listing(ctx, series)
	if err != nil {
		return nil, err
	}
	detail := &SeriesDetail{Series: series, Years: years(listing)}
	if len(listing) > 0 {
		detail.FirstAirDate = listing[0].AirDate
		detail.LastAirDate = listing[len(listing)-1].AirDate
	}
	return detail, nil
}

func years(listing []NumberedUpload) []int {
	var out []int
	for _, upload := range listing {
		if len(out) == 0 || out[len(out)-1] != upload.Season {
			out = append(out, upload.Season)
		}
	}
	return out
}

// Season is one year of uploads.
type Season struct {
	Number  int
	AirDate string
	Count   int
}

// Seasons lists the years a series has uploads in.
func (p *Provider) Seasons(ctx context.Context, id string) ([]Season, error) {
	series, err := p.SeriesByID(ctx, id)
	if err != nil || series == nil {
		return nil, err
	}
	listing, err := p.Listing(ctx, series)
	if err != nil {
		return nil, err
	}
	var seasons []Season
	for _, upload := range listing {
		if len(seasons) == 0 || seasons[len(seasons)-1].Number != upload.Season {
			seasons = append(seasons, Season{Number: upload.Season, AirDate: upload.AirDate})
		}
		seasons[len(seasons)-1].Count++
	}
	return seasons, nil
}

// Episode is one video as a Silo episode.
type Episode struct {
	VideoID  string
	Season   int
	Number   int
	Title    string
	Overview string
	AirDate  string
	// RuntimeMinutes is rounded up, so a 30-second video still has a runtime.
	RuntimeMinutes int
	StillURL       string
}

// Episodes returns a season's episodes. season <= 0 returns every season.
func (p *Provider) Episodes(ctx context.Context, id string, season int) ([]Episode, error) {
	series, err := p.SeriesByID(ctx, id)
	if err != nil || series == nil {
		return nil, err
	}
	listing, err := p.Listing(ctx, series)
	if err != nil {
		return nil, err
	}
	selected := make([]NumberedUpload, 0, len(listing))
	for _, upload := range listing {
		if season <= 0 || upload.Season == season {
			selected = append(selected, upload)
		}
	}
	ids := make([]string, len(selected))
	for i, upload := range selected {
		ids[i] = upload.VideoID
	}
	details, err := p.videoDetails(ctx, ids)
	if err != nil {
		return nil, err
	}
	episodes := make([]Episode, 0, len(selected))
	for _, upload := range selected {
		episode := Episode{
			VideoID: upload.VideoID,
			Season:  upload.Season,
			Number:  upload.Episode,
			Title:   upload.Title,
			AirDate: upload.AirDate,
		}
		if video, ok := details[upload.VideoID]; ok {
			episode.Title = video.Snippet.Title
			episode.Overview = strings.TrimSpace(video.Snippet.Description)
			episode.RuntimeMinutes = RuntimeMinutes(video.ContentDetails.Duration)
			episode.StillURL = video.Snippet.Thumbnails.Best().URL
		}
		episodes = append(episodes, episode)
	}
	sort.SliceStable(episodes, func(i, j int) bool {
		if episodes[i].Season != episodes[j].Season {
			return episodes[i].Season < episodes[j].Season
		}
		return episodes[i].Number < episodes[j].Number
	})
	return episodes, nil
}

// RuntimeMinutes converts an ISO 8601 duration to whole minutes, rounded up
// so a 30-second video still has a runtime.
func RuntimeMinutes(iso string) int {
	d := youtube.ParseDuration(iso)
	if d <= 0 {
		return 0
	}
	return int(math.Ceil(d.Minutes()))
}

// Video returns one video's details; ok false when YouTube does not have it.
func (p *Provider) Video(ctx context.Context, id string) (youtube.Video, bool, error) {
	details, err := p.videoDetails(ctx, []string{id})
	if err != nil {
		return youtube.Video{}, false, err
	}
	video, ok := details[id]
	return video, ok, nil
}

// videoDetails fetches the videos not cached yet, 50 per call.
func (p *Provider) videoDetails(ctx context.Context, ids []string) (map[string]youtube.Video, error) {
	out := make(map[string]youtube.Video, len(ids))
	var missing []string
	now := p.now()
	p.mu.Lock()
	for _, id := range ids {
		if entry, ok := p.videos[id]; ok && !now.After(entry.expires) {
			out[id] = entry.value
		} else {
			missing = append(missing, id)
		}
	}
	p.mu.Unlock()
	if len(missing) == 0 {
		return out, nil
	}
	videos, err := p.yt.Videos(ctx, missing)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	for _, video := range videos {
		out[video.ID] = video
		p.videos[video.ID] = cached[youtube.Video]{value: video, expires: now.Add(detailTTL)}
	}
	p.mu.Unlock()
	return out, nil
}
