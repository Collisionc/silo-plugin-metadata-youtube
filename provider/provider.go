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
	"unicode"

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
	yt       *youtube.Client
	loc      *time.Location
	mode     SeasonMode
	backdrop Backdrop
	now      func() time.Time

	mu       sync.Mutex
	series   map[string]cached[*Series]
	listings map[string]cached[[]NumberedUpload]
	videos   map[string]cached[youtube.Video]
}

type cached[T any] struct {
	value   T
	expires time.Time
}

// Backdrop chooses a series' background art.
type Backdrop string

const (
	// NoBackdrop leaves the series without background art, so its text sits
	// on a plain background. The default: channel banners and thumbnails are
	// full of logos and lettering that make the overview hard to read.
	NoBackdrop Backdrop = "none"
	// BannerBackdrop uses the channel's banner.
	BannerBackdrop Backdrop = "banner"
	// VideoBackdrop uses the newest upload's thumbnail.
	VideoBackdrop Backdrop = "video"
)

// ParseBackdrop reads a configured backdrop; unknown or empty means none.
func ParseBackdrop(value string) Backdrop {
	switch Backdrop(value) {
	case BannerBackdrop, VideoBackdrop:
		return Backdrop(value)
	}
	return NoBackdrop
}

// Option adjusts a Provider.
type Option func(*Provider)

// WithSeasonMode sets how uploads are split into seasons.
func WithSeasonMode(mode SeasonMode) Option { return func(p *Provider) { p.mode = mode } }

// WithBackdrop sets the series background art.
func WithBackdrop(backdrop Backdrop) Option { return func(p *Provider) { p.backdrop = backdrop } }

// New returns a provider using client, dating uploads in loc (UTC when nil).
// By default seasons are years and series have no backdrop.
func New(client *youtube.Client, loc *time.Location, options ...Option) *Provider {
	if loc == nil {
		loc = time.UTC
	}
	p := &Provider{
		yt:       client,
		loc:      loc,
		mode:     SeasonsByYear,
		backdrop: NoBackdrop,
		now:      time.Now,
		series:   make(map[string]cached[*Series]),
		listings: make(map[string]cached[[]NumberedUpload]),
		videos:   make(map[string]cached[youtube.Video]),
	}
	for _, option := range options {
		option(p)
	}
	return p
}

// Series is a channel or playlist as a Silo series.
type Series struct {
	ID          string
	Kind        youtube.RefKind // RefChannel or RefPlaylist
	Title       string
	Overview    string
	ChannelID   string // the channel itself, or the playlist's owner
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
	// Aliases are other titles the hit is known by. Silo accepts a search
	// result only when its title, or one of these, resembles the title on disk
	// closely enough; "BrainBreakLand [UCA94TZluH5eQtkQpaCZQfRw]" scores far
	// too low against "Brain Break Land" on its own.
	Aliases []string
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
		return []SearchHit{series.identified(query)}, nil
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
		return series.namedHits(query), nil
	}

	if series, err := p.repairedSeries(ctx, query); err != nil || series != nil {
		if series == nil {
			return nil, err
		}
		return series.namedHits(query), nil
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
		hit := series.hit()
		// The folder is the channel's name run together or punctuated
		// differently ("BrainBreakLand" for "Brain Break Land"): the same
		// name, which Silo's title comparison would not see.
		if compact(text) != "" && compact(text) == compact(series.Title) && text != series.Title {
			hit.Aliases = []string{text}
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// namedHits answers a query that named a series outright. A channel comes
// with its uploads playlist as a second, real candidate, and a playlist with
// its owner's uploads playlist.
//
// Silo accepts a lone search result automatically only at a match score of
// 70. A YouTube channel cannot reach that: an exact title is worth 45, one
// provider 12 and having an ID 5, while the points for a matching year, a
// trusted TMDB/TVDB/IMDb ID or a second provider never apply to it. With
// several candidates Silo instead takes the best when it scores at least 55
// and leads the next by 15. The channel, carrying the folder title as an
// exact alias, scores 62; its uploads playlist, "Uploads from <channel>",
// scores far lower against the folder title, so the channel wins. Choosing
// the playlist by hand in Silo's Match dialog also works: it lists the same
// videos. A named playlist wins over its owner's uploads the same way.
func (s *Series) namedHits(query string) []SearchHit {
	hits := []SearchHit{s.identified(query)}
	if uploads := s.uploadsSeries(); uploads != nil {
		hits = append(hits, uploads.hit())
	} else if uploads := s.ownerUploads(); uploads != nil {
		hits = append(hits, uploads.hit())
	}
	return hits
}

// ownerUploads is the uploads playlist of the channel owning a playlist; nil
// for a channel, an ownerless playlist (a mix) or an uploads playlist itself.
// It is only a search candidate: chosen, SeriesByID builds it from the
// channel.
func (s *Series) ownerUploads() *Series {
	if s.Kind != youtube.RefPlaylist || s.ChannelName == "" {
		return nil
	}
	id := youtube.UploadsPlaylistFor(s.ChannelID)
	if id == "" || id == s.ID {
		return nil
	}
	return &Series{
		ID:          id,
		Kind:        youtube.RefPlaylist,
		Title:       "Uploads from " + s.ChannelName,
		ChannelID:   s.ChannelID,
		ChannelName: s.ChannelName,
		PlaylistID:  id,
	}
}

// uploadsSeries is a channel's uploads playlist as a series of its own; nil
// for a playlist.
func (s *Series) uploadsSeries() *Series {
	if s.Kind != youtube.RefChannel || s.PlaylistID == "" || s.PlaylistID == s.ID {
		return nil
	}
	uploads := *s
	uploads.ID = s.PlaylistID
	uploads.Kind = youtube.RefPlaylist
	uploads.Title = "Uploads from " + s.Title
	uploads.BannerURL = ""
	return &uploads
}

// identified is the hit for a series the query named outright, by an ID,
// URL or @handle in it, or a YouTube ID Silo already stored. The folder title
// itself then names this series, so it goes back as an alias, with and
// without its bracketed tags, and Silo's title comparison accepts the match.
func (s *Series) identified(query string) SearchHit {
	hit := s.hit()
	seen := map[string]bool{s.Title: true}
	for _, alias := range []string{strings.TrimSpace(query), youtube.CleanQuery(query)} {
		if alias != "" && !seen[alias] {
			seen[alias] = true
			hit.Aliases = append(hit.Aliases, alias)
		}
	}
	return hit
}

// compact lowercases s and keeps only its letters and digits.
func compact(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// repairedSeries finds the series behind a tag whose underscores Silo turned
// into spaces ([UC8WBLj aquoUjJkXPXMC5aA]): it looks every rebuilt candidate
// up in one call per kind (1 quota unit each) and takes the one that exists.
func (p *Provider) repairedSeries(ctx context.Context, query string) (*Series, error) {
	var channels, playlists []string
	for _, ref := range youtube.RepairedRefs(query) {
		if ref.Kind == youtube.RefChannel {
			channels = append(channels, ref.ID)
		} else {
			playlists = append(playlists, ref.ID)
		}
	}
	if len(playlists) > 0 {
		found, err := p.yt.Playlists(ctx, playlists)
		if err != nil {
			return nil, err
		}
		if len(found) > 0 {
			series := seriesFromPlaylist(found[0])
			p.storeSeries(series)
			return series, nil
		}
	}
	if len(channels) > 0 {
		found, err := p.yt.Channels(ctx, channels)
		if err != nil {
			return nil, err
		}
		if len(found) > 0 {
			series := seriesFromChannel(found[0])
			p.storeSeries(series)
			return series, nil
		}
	}
	return nil, nil
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
	// A channel's uploads playlist (UU...), offered by namedHits: built from
	// the channel, as the playlist's own record carries no artwork.
	if channelID := channelForUploads(id); channelID != "" {
		channels, err := p.yt.Channels(ctx, []string{channelID})
		if err != nil || len(channels) == 0 {
			return nil, err
		}
		channel := seriesFromChannel(channels[0])
		p.storeSeries(channel)
		if uploads := channel.uploadsSeries(); uploads != nil && uploads.ID == id {
			p.storeSeries(uploads)
			return uploads, nil
		}
	}
	playlists, err := p.yt.Playlists(ctx, []string{id})
	if err != nil || len(playlists) == 0 {
		return nil, err
	}
	series := seriesFromPlaylist(playlists[0])
	p.storeSeries(series)
	return series, nil
}

// channelForUploads returns the channel an uploads playlist ID belongs to.
func channelForUploads(id string) string {
	if len(id) == 24 && strings.HasPrefix(id, "UU") {
		if channel := "UC" + id[2:]; youtube.IsChannelID(channel) {
			return channel
		}
	}
	return ""
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
		ChannelID:   ch.ID,
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
		ChannelID:   pl.Snippet.ChannelID,
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

// channelBanner asks for the whole banner, at most 2560 px wide, as the
// series backdrop. Without a size suffix the image server returns a 512 px
// thumbnail. "=w2560" never enlarges: a banner uploaded at 2048x1152 comes
// back at that size (checked 2026-11-09). The longer crop suffixes YouTube's
// own pages use are fussy: one with "-nfpr" answers HTTP 400.
func channelBanner(url string) string {
	if url == "" || strings.Contains(url, "=") {
		return url
	}
	return url + "=w2560"
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
	numbered := Number(uploads, p.loc, p.mode)
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
	// Years are the years with uploads, oldest first.
	Years []int
	// Seasons is how many seasons the series has: len(Years), or 1 in
	// SingleSeason mode.
	Seasons      int
	FirstAirDate string
	LastAirDate  string
	// BackdropURL is the configured background art; empty for none.
	BackdropURL string
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
	detail := &SeriesDetail{Series: series, Years: years(listing, p.loc)}
	if len(listing) > 0 {
		detail.FirstAirDate = listing[0].AirDate
		detail.LastAirDate = listing[len(listing)-1].AirDate
		detail.Seasons = len(detail.Years)
		if p.mode == SingleSeason {
			detail.Seasons = 1
		}
	}
	backdrop, err := p.Backdrop(ctx, series)
	if err != nil {
		return nil, err
	}
	detail.BackdropURL = backdrop
	return detail, nil
}

func years(listing []NumberedUpload, loc *time.Location) []int {
	var out []int
	for _, upload := range listing {
		year := upload.Published.In(loc).Year()
		if len(out) == 0 || out[len(out)-1] != year {
			out = append(out, year)
		}
	}
	return out
}

// Backdrop returns the series' background art as configured; empty for none.
// The video backdrop is the newest upload's largest thumbnail (one API unit
// the first time, then cached).
func (p *Provider) Backdrop(ctx context.Context, series *Series) (string, error) {
	switch p.backdrop {
	case BannerBackdrop:
		return series.BannerURL, nil
	case VideoBackdrop:
		listing, err := p.Listing(ctx, series)
		if err != nil || len(listing) == 0 {
			return "", err
		}
		newest := listing[len(listing)-1].VideoID
		details, err := p.videoDetails(ctx, []string{newest})
		if err != nil {
			return "", err
		}
		return details[newest].Snippet.Thumbnails.Best().URL, nil
	}
	return "", nil
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
