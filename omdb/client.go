// Package omdb asks OMDb (www.omdbapi.com), a database built from IMDb's
// data, one question: is there a series named exactly as a YouTube channel.
// OMDb knows nothing of YouTube, so the answer is a match by name, a guess
// the plugin only makes when asked to.
package omdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// DefaultBaseURL is the OMDb API endpoint.
const DefaultBaseURL = "https://www.omdbapi.com/"

const maxResponseBody = 1 << 20

var (
	// ErrNoAPIKey is returned by every call when no API key is configured.
	ErrNoAPIKey = errors.New("omdb: no API key configured")
	// ErrKeyRefused means OMDb refused the API key, or the key has used up
	// its requests for the day (1,000 for a free key).
	ErrKeyRefused = errors.New("omdb: the API key was refused or is out of requests for today")
	// ErrRequestFailed marks a call that got no usable answer from OMDb.
	ErrRequestFailed = errors.New("omdb: request failed")
)

var titleIDRe = regexp.MustCompile(`^tt[0-9]{7,10}$`)

// Client calls OMDb with one API key.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient returns a client for baseURL (DefaultBaseURL when empty).
func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{baseURL: baseURL, apiKey: apiKey, http: httpClient}
}

// Series is one series of an OMDb search.
type Series struct {
	IMDbID string
	Title  string
	// Year is the year the series began; 0 when OMDb does not say.
	Year int
}

// SeriesNamed returns the series OMDb lists under exactly name, compared by
// letters and digits alone ("DEATH BATTLE!" is "Death Battle"), that began in
// notBefore or later (any year when notBefore is 0). It reads the first page
// of OMDb's search, ten results, which OMDb opens with the closest names.
func (c *Client) SeriesNamed(ctx context.Context, name string, notBefore int) ([]Series, error) {
	want := compact(name)
	if len([]rune(want)) < 2 {
		return nil, nil // nothing to tell one series from another by
	}
	var out struct {
		envelope
		Search []struct {
			Title  string `json:"Title"`
			Year   string `json:"Year"`
			IMDbID string `json:"imdbID"`
			Type   string `json:"Type"`
		} `json:"Search"`
	}
	found, err := c.get(ctx, url.Values{"s": {strings.TrimSpace(name)}, "type": {"series"}}, &out, &out.envelope)
	if err != nil || !found {
		return nil, err
	}
	var series []Series
	seen := make(map[string]bool)
	for _, hit := range out.Search {
		id := strings.ToLower(strings.TrimSpace(hit.IMDbID))
		year := firstYear(hit.Year)
		if hit.Type != "series" || !titleIDRe.MatchString(id) || seen[id] || compact(hit.Title) != want {
			continue
		}
		if notBefore > 0 && year < notBefore {
			continue
		}
		seen[id] = true
		series = append(series, Series{IMDbID: id, Title: hit.Title, Year: year})
	}
	return series, nil
}

// checkKeyTitle is The Matrix, a lookup that always works.
const checkKeyTitle = "tt0133093"

// CheckKey makes one request to find out whether OMDb accepts the key.
func (c *Client) CheckKey(ctx context.Context) error {
	var out envelope
	_, err := c.get(ctx, url.Values{"i": {checkKeyTitle}}, &out, &out)
	return err
}

// envelope carries OMDb's success flag and error, which it sends with HTTP
// 200.
type envelope struct {
	Response string `json:"Response"`
	Error    string `json:"Error"`
}

// get makes one request. found is false when OMDb answers that it has
// nothing: no such title, or a name too common to search for.
func (c *Client) get(ctx context.Context, params url.Values, dest any, env *envelope) (found bool, err error) {
	if c == nil || c.apiKey == "" {
		return false, ErrNoAPIKey
	}
	params.Set("apikey", c.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return false, ErrRequestFailed
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// The URL carries the key; never put it in an error or a log.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return false, fmt.Errorf("%w: %w", ErrRequestFailed, urlErr.Err)
		}
		return false, ErrRequestFailed
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return false, fmt.Errorf("%w: reading response: %w", ErrRequestFailed, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return false, ErrKeyRefused
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return false, fmt.Errorf("%w: HTTP %d", ErrRequestFailed, resp.StatusCode)
	case len(body) > maxResponseBody:
		return false, fmt.Errorf("%w: response larger than %d bytes", ErrRequestFailed, maxResponseBody)
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return false, fmt.Errorf("%w: decoding response: %w", ErrRequestFailed, err)
	}
	if env.Response == "True" {
		return true, nil
	}
	switch env.Error {
	case "Movie not found!", "Series not found!", "Incorrect IMDb ID.", "Too many results.":
		return false, nil
	case "Invalid API key!", "Request limit reached!", "No API key provided.":
		return false, ErrKeyRefused
	}
	return false, fmt.Errorf("%w: %s", ErrRequestFailed, env.Error)
}

// firstYear reads "2010" and a series' "2010–2015" or "2010–".
func firstYear(s string) int {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return 0
	}
	year, err := strconv.Atoi(s[:4])
	if err != nil {
		return 0
	}
	return year
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
