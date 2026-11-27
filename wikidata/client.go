// Package wikidata asks Wikidata one question: which IMDb title lists a
// YouTube channel or playlist as its own. YouTube's API has no IMDb IDs;
// Wikidata records both for a few thousand shows.
package wikidata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultEndpoint is the Wikidata Query Service.
const DefaultEndpoint = "https://query.wikidata.org/sparql"

const (
	// Wikidata properties: an item's YouTube channel ID, its YouTube playlist
	// ID and its IMDb ID.
	channelProperty  = "P2397"
	playlistProperty = "P4300"
	imdbProperty     = "P345"

	maxResponseBody = 1 << 20
)

var (
	// A channel ID is UC and 22 characters; a playlist ID is anything else of
	// the same alphabet. Only such a value is ever put into a query.
	channelIDRe  = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	playlistIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{12,64}$`)
	// An IMDb title ID. Wikidata's IMDb property also holds people (nm...)
	// and companies (co...), which are not what a series is.
	titleIDRe = regexp.MustCompile(`^tt[0-9]{7,10}$`)
)

// ErrRequestFailed marks a lookup that got no usable answer from Wikidata.
var ErrRequestFailed = errors.New("wikidata: request failed")

// Client queries the Wikidata Query Service. It needs no key.
type Client struct {
	endpoint  string
	userAgent string
	http      *http.Client
}

// NewClient returns a client for endpoint (DefaultEndpoint when empty).
// Wikimedia asks every client to name itself in userAgent.
func NewClient(endpoint, userAgent string, httpClient *http.Client) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{endpoint: endpoint, userAgent: userAgent, http: httpClient}
}

// IsTitleID reports whether id has the shape of an IMDb title ID (tt1234567).
func IsTitleID(id string) bool { return titleIDRe.MatchString(id) }

// IMDbTitle returns the IMDb title ID of the Wikidata item that lists
// youtubeID as its YouTube channel (UC...) or playlist. It returns "" when no
// item does, and also when several titles do: a network's channel can be
// listed by each of its shows, and then none of them is the channel.
func (c *Client) IMDbTitle(ctx context.Context, youtubeID string) (string, error) {
	property := playlistProperty
	switch {
	case channelIDRe.MatchString(youtubeID):
		property = channelProperty
	case !playlistIDRe.MatchString(youtubeID):
		return "", nil
	}
	query := fmt.Sprintf(`SELECT DISTINCT ?imdb WHERE { ?item wdt:%s "%s" ; wdt:%s ?imdb . } LIMIT 50`, property, youtubeID, imdbProperty)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"?"+url.Values{"query": {query}, "format": {"json"}}.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/sparql-results+json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRequestFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return "", fmt.Errorf("%w: reading response: %w", ErrRequestFailed, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: HTTP %d", ErrRequestFailed, resp.StatusCode)
	}
	if len(body) > maxResponseBody {
		return "", fmt.Errorf("%w: response larger than %d bytes", ErrRequestFailed, maxResponseBody)
	}
	var out struct {
		Results struct {
			Bindings []struct {
				IMDb struct {
					Value string `json:"value"`
				} `json:"imdb"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("%w: decoding response: %w", ErrRequestFailed, err)
	}
	title := ""
	for _, binding := range out.Results.Bindings {
		id := strings.ToLower(strings.TrimSpace(binding.IMDb.Value))
		if !IsTitleID(id) || id == title {
			continue
		}
		if title != "" {
			return "", nil // several titles: not one channel's own
		}
		title = id
	}
	return title, nil
}
