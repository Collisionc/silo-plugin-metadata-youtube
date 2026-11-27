// Package youtube is a small client for the parts of the YouTube Data API v3
// the plugin reads: channels, playlists, playlist items, videos and channel
// search. Every call costs quota from the API key's project (10,000 units a
// day by default): search costs 100 units, every other call 1 unit per page of
// up to 50 results.
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the YouTube Data API v3 endpoint.
const DefaultBaseURL = "https://www.googleapis.com/youtube/v3"

const (
	maxPageSize = 50
	// maxPlaylistPages bounds one listing at 25,000 videos, so a mistyped ID
	// for an enormous playlist cannot spend the whole day's quota.
	maxPlaylistPages = 500
	maxResponseBody  = 8 << 20
	maxAttempts      = 3
)

// Client calls the YouTube Data API with an API key.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	// backoff is the wait before retry n (1-based); tests shorten it.
	backoff func(attempt int) time.Duration
}

// NewClient returns a client for baseURL (DefaultBaseURL when empty).
func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    httpClient,
		backoff: func(attempt int) time.Duration { return time.Duration(1<<(attempt-1)) * time.Second },
	}
}

// HasKey reports whether an API key is configured.
func (c *Client) HasKey() bool { return c != nil && c.apiKey != "" }

// ErrNoAPIKey is returned by every call when no API key is configured.
var ErrNoAPIKey = errors.New("youtube: no API key configured (Admin > Plugins > YouTube Metadata)")

// ErrRequestFailed marks a call that got no answer from the API: the
// connection failed or broke off. Trying again later can help.
var ErrRequestFailed = errors.New("youtube: request failed")

// APIError is a non-2xx answer from the API.
type APIError struct {
	Status  int
	Reason  string
	Message string
	// Detail is the google.rpc.ErrorInfo reason Google adds to some errors,
	// such as API_KEY_INVALID or SERVICE_DISABLED.
	Detail string
}

func (e *APIError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("youtube: HTTP %d %s: %s", e.Status, e.Reason, e.Message)
	}
	return fmt.Sprintf("youtube: HTTP %d: %s", e.Status, e.Message)
}

// QuotaExceeded reports whether the key's daily quota is used up. Retrying
// does not help until the quota resets (midnight Pacific time).
func (e *APIError) QuotaExceeded() bool {
	switch e.Reason {
	case "quotaExceeded", "dailyLimitExceeded":
		return true
	}
	return false
}

// RateLimited reports whether the API asked for fewer calls per second (not
// the daily quota, which QuotaExceeded reports).
func (e *APIError) RateLimited() bool {
	return e.Status == http.StatusTooManyRequests || e.Reason == "rateLimitExceeded" || e.Reason == "userRateLimitExceeded"
}

// KeyRejected reports whether the API refused the key itself: it is not a
// valid key, has expired, is restricted to other callers, or belongs to a
// project where the YouTube Data API is not enabled. Only a new key or a
// change in the Google Cloud console helps.
func (e *APIError) KeyRejected() bool {
	if e.Status != http.StatusBadRequest && e.Status != http.StatusUnauthorized && e.Status != http.StatusForbidden {
		return false
	}
	switch {
	case strings.HasPrefix(e.Detail, "API_KEY_"), e.Detail == "SERVICE_DISABLED":
		return true
	case e.Reason == "keyInvalid", e.Reason == "keyExpired", e.Reason == "accessNotConfigured", e.Reason == "ipRefererBlocked":
		return true
	}
	return strings.Contains(strings.ToLower(e.Message), "api key")
}

func (e *APIError) retryable() bool {
	if e.QuotaExceeded() {
		return false
	}
	return e.RateLimited() || e.Status >= 500
}

type errorBody struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Details []struct {
			Type   string `json:"@type"`
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

func (c *Client) get(ctx context.Context, resource string, params url.Values, dest any) error {
	if !c.HasKey() {
		return ErrNoAPIKey
	}
	query := url.Values{}
	for key, values := range params {
		query[key] = values
	}
	query.Set("key", c.apiKey)
	endpoint := c.baseURL + "/" + resource + "?" + query.Encode()

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.backoff(attempt - 1)):
			}
		}
		err := c.do(ctx, endpoint, dest)
		if err == nil {
			return nil
		}
		lastErr = err
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if !apiErr.retryable() {
				return err
			}
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A transport error: retry.
	}
	return lastErr
}

func (c *Client) do(ctx context.Context, endpoint string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// The URL carries the key; never put it in an error or a log.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("%w: %w", ErrRequestFailed, urlErr.Err)
		}
		return ErrRequestFailed
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return fmt.Errorf("%w: reading response: %w", ErrRequestFailed, err)
	}
	if len(body) > maxResponseBody {
		return fmt.Errorf("youtube: response larger than %d bytes", maxResponseBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
		var parsed errorBody
		if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
			apiErr.Message = parsed.Error.Message
			if len(parsed.Error.Errors) > 0 {
				apiErr.Reason = parsed.Error.Errors[0].Reason
			}
			for _, detail := range parsed.Error.Details {
				if strings.HasSuffix(detail.Type, "google.rpc.ErrorInfo") {
					apiErr.Detail = detail.Reason
				}
			}
		}
		return apiErr
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("youtube: decoding response: %w", err)
	}
	return nil
}

// Thumbnail is one size of an image.
type Thumbnail struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Thumbnails are an item's images by size name.
type Thumbnails map[string]Thumbnail

// Best returns the largest image: maxres, standard, high, medium, default.
func (t Thumbnails) Best() Thumbnail {
	for _, name := range []string{"maxres", "standard", "high", "medium", "default"} {
		if thumb, ok := t[name]; ok && thumb.URL != "" {
			return thumb
		}
	}
	return Thumbnail{}
}

// Channel is a YouTube channel.
type Channel struct {
	ID      string `json:"id"`
	Snippet struct {
		Title       string     `json:"title"`
		Description string     `json:"description"`
		CustomURL   string     `json:"customUrl"`
		PublishedAt string     `json:"publishedAt"`
		Thumbnails  Thumbnails `json:"thumbnails"`
		Country     string     `json:"country"`
	} `json:"snippet"`
	ContentDetails struct {
		RelatedPlaylists struct {
			Uploads string `json:"uploads"`
		} `json:"relatedPlaylists"`
	} `json:"contentDetails"`
	BrandingSettings struct {
		Image struct {
			BannerExternalURL string `json:"bannerExternalUrl"`
		} `json:"image"`
	} `json:"brandingSettings"`
}

// UploadsPlaylistID is the playlist holding every public upload of a channel.
func (ch Channel) UploadsPlaylistID() string {
	if id := ch.ContentDetails.RelatedPlaylists.Uploads; id != "" {
		return id
	}
	return UploadsPlaylistFor(ch.ID)
}

// UploadsPlaylistFor derives a channel's uploads playlist (UC... -> UU...).
func UploadsPlaylistFor(channelID string) string {
	if strings.HasPrefix(channelID, "UC") {
		return "UU" + channelID[2:]
	}
	return ""
}

const channelParts = "snippet,contentDetails,brandingSettings"

type channelList struct {
	Items []Channel `json:"items"`
}

// Channels looks channels up by ID, at most 50 at a time.
func (c *Client) Channels(ctx context.Context, ids []string) ([]Channel, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > maxPageSize {
		return nil, fmt.Errorf("youtube: at most %d channel IDs per call", maxPageSize)
	}
	var out channelList
	err := c.get(ctx, "channels", url.Values{"part": {channelParts}, "id": {strings.Join(ids, ",")}, "maxResults": {fmt.Sprint(maxPageSize)}}, &out)
	return out.Items, err
}

// ChannelByHandle looks a channel up by its @handle; nil when none exists.
func (c *Client) ChannelByHandle(ctx context.Context, handle string) (*Channel, error) {
	handle = strings.TrimPrefix(strings.TrimSpace(handle), "@")
	if handle == "" {
		return nil, nil
	}
	var out channelList
	if err := c.get(ctx, "channels", url.Values{"part": {channelParts}, "forHandle": {"@" + handle}}, &out); err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, nil
	}
	return &out.Items[0], nil
}

// checkKeyChannel is YouTube's own channel, a lookup that always works.
const checkKeyChannel = "UCBR8-60-B28hp2BmDPdntcQ"

// CheckKey makes the cheapest call there is, 1 quota unit, to find out
// whether the API accepts the key.
func (c *Client) CheckKey(ctx context.Context) error {
	var out channelList
	return c.get(ctx, "channels", url.Values{"part": {"id"}, "id": {checkKeyChannel}}, &out)
}

// SearchChannels returns the IDs of the channels best matching query, best
// first. It costs 100 quota units.
func (c *Client) SearchChannels(ctx context.Context, query string, limit int) ([]string, error) {
	if limit <= 0 || limit > maxPageSize {
		limit = 5
	}
	var out struct {
		Items []struct {
			ID struct {
				ChannelID string `json:"channelId"`
			} `json:"id"`
		} `json:"items"`
	}
	if err := c.get(ctx, "search", url.Values{"part": {"id"}, "type": {"channel"}, "q": {query}, "maxResults": {fmt.Sprint(limit)}}, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Items))
	for _, item := range out.Items {
		if item.ID.ChannelID != "" {
			ids = append(ids, item.ID.ChannelID)
		}
	}
	return ids, nil
}

// Playlist is a YouTube playlist.
type Playlist struct {
	ID      string `json:"id"`
	Snippet struct {
		Title        string     `json:"title"`
		Description  string     `json:"description"`
		ChannelID    string     `json:"channelId"`
		ChannelTitle string     `json:"channelTitle"`
		PublishedAt  string     `json:"publishedAt"`
		Thumbnails   Thumbnails `json:"thumbnails"`
	} `json:"snippet"`
}

// Playlists looks playlists up by ID, at most 50 at a time.
func (c *Client) Playlists(ctx context.Context, ids []string) ([]Playlist, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > maxPageSize {
		return nil, fmt.Errorf("youtube: at most %d playlist IDs per call", maxPageSize)
	}
	var out struct {
		Items []Playlist `json:"items"`
	}
	err := c.get(ctx, "playlists", url.Values{"part": {"snippet"}, "id": {strings.Join(ids, ",")}, "maxResults": {fmt.Sprint(maxPageSize)}}, &out)
	return out.Items, err
}

// PlaylistItem is one video's entry in a playlist.
type PlaylistItem struct {
	Snippet struct {
		Title       string `json:"title"`
		PublishedAt string `json:"publishedAt"`
	} `json:"snippet"`
	ContentDetails struct {
		VideoID          string `json:"videoId"`
		VideoPublishedAt string `json:"videoPublishedAt"`
	} `json:"contentDetails"`
	Status struct {
		PrivacyStatus string `json:"privacyStatus"`
	} `json:"status"`
}

// PlaylistItems lists every entry of a playlist, one call per 50 entries.
func (c *Client) PlaylistItems(ctx context.Context, playlistID string) ([]PlaylistItem, error) {
	var all []PlaylistItem
	pageToken := ""
	for page := 0; page < maxPlaylistPages; page++ {
		params := url.Values{"part": {"snippet,contentDetails,status"}, "playlistId": {playlistID}, "maxResults": {fmt.Sprint(maxPageSize)}}
		if pageToken != "" {
			params.Set("pageToken", pageToken)
		}
		var out struct {
			NextPageToken string         `json:"nextPageToken"`
			Items         []PlaylistItem `json:"items"`
		}
		if err := c.get(ctx, "playlistItems", params, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Items...)
		if out.NextPageToken == "" {
			return all, nil
		}
		pageToken = out.NextPageToken
	}
	return all, nil
}

// Video is one YouTube video.
type Video struct {
	ID      string `json:"id"`
	Snippet struct {
		Title        string     `json:"title"`
		Description  string     `json:"description"`
		PublishedAt  string     `json:"publishedAt"`
		ChannelID    string     `json:"channelId"`
		ChannelTitle string     `json:"channelTitle"`
		Thumbnails   Thumbnails `json:"thumbnails"`
		Tags         []string   `json:"tags"`
	} `json:"snippet"`
	ContentDetails struct {
		Duration string `json:"duration"`
	} `json:"contentDetails"`
	Status struct {
		PrivacyStatus string `json:"privacyStatus"`
	} `json:"status"`
}

// Videos looks videos up by ID, 50 per call. Videos that no longer exist or
// are private are simply absent from the result.
func (c *Client) Videos(ctx context.Context, ids []string) ([]Video, error) {
	var all []Video
	for start := 0; start < len(ids); start += maxPageSize {
		end := min(start+maxPageSize, len(ids))
		var out struct {
			Items []Video `json:"items"`
		}
		if err := c.get(ctx, "videos", url.Values{"part": {"snippet,contentDetails,status"}, "id": {strings.Join(ids[start:end], ",")}, "maxResults": {fmt.Sprint(maxPageSize)}}, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Items...)
	}
	return all, nil
}
