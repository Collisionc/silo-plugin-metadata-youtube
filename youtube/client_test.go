package youtube

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func errorServer(t *testing.T, status int, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL, "k", server.Client())
}

// Google's errors name the key's problem in an ErrorInfo detail.
func TestAPIErrorsSayWhatWentWrong(t *testing.T) {
	cases := []struct {
		name, body                      string
		status                          int
		quota, rateLimited, keyRejected bool
	}{
		{"invalid key", `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","errors":[{"reason":"badRequest"}],"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID"}]}}`, 400, false, false, true},
		{"API not enabled", `{"error":{"code":403,"message":"YouTube Data API v3 has not been used in project 1 before or it is disabled.","errors":[{"reason":"accessNotConfigured"}],"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED"}]}}`, 403, false, false, true},
		{"quota", `{"error":{"code":403,"message":"You have exceeded your quota.","errors":[{"reason":"quotaExceeded"}]}}`, 403, true, false, false},
		{"private playlist", `{"error":{"code":403,"message":"The playlist is private.","errors":[{"reason":"playlistItemsNotAccessible"}]}}`, 403, false, false, false},
	}
	for _, tc := range cases {
		err := errorServer(t, tc.status, tc.body).CheckKey(context.Background())
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("%s: err = %v, want an APIError", tc.name, err)
		}
		if apiErr.QuotaExceeded() != tc.quota || apiErr.RateLimited() != tc.rateLimited || apiErr.KeyRejected() != tc.keyRejected {
			t.Errorf("%s: quota %v, rate limited %v, key rejected %v", tc.name, apiErr.QuotaExceeded(), apiErr.RateLimited(), apiErr.KeyRejected())
		}
	}
}

// A call that reaches no server is marked as such, so the plugin can tell
// Silo to try again later.
func TestUnreachableAPIIsARequestFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	client := NewClient(url, "k", nil)
	client.backoff = func(int) time.Duration { return 0 }
	if err := client.CheckKey(context.Background()); !errors.Is(err, ErrRequestFailed) {
		t.Fatalf("err = %v, want ErrRequestFailed", err)
	}
}

// An answer that is not the API's JSON, such as a proxy's error page, is no
// answer: it is asked again, and then reported as a failed request, which
// Silo retries. Reported as anything else it counts as permanent, and three
// of those park a folder.
func TestUnreadableAnswerIsARequestFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.URL, "k", server.Client())
	client.backoff = func(int) time.Duration { return 0 }
	if err := client.CheckKey(context.Background()); !errors.Is(err, ErrRequestFailed) {
		t.Fatalf("err = %v, want ErrRequestFailed", err)
	}
	if calls != maxAttempts {
		t.Errorf("%d requests, want %d", calls, maxAttempts)
	}
}

// Every call asks for the values the plugin reads and no others: a listing's
// page is otherwise mostly descriptions and thumbnails it never uses.
func TestEveryCallNamesItsFields(t *testing.T) {
	asked := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked[r.URL.Path[1:]] = r.URL.Query().Get("fields")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	t.Cleanup(server.Close)
	c := NewClient(server.URL, "k", server.Client())
	ctx := context.Background()
	_, _ = c.Channels(ctx, []string{checkKeyChannel})
	_, _ = c.Playlists(ctx, []string{"PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat"})
	_, _ = c.PlaylistItems(ctx, "UUBR8-60-B28hp2BmDPdntcQ")
	_, _ = c.Videos(ctx, []string{"0bNNs5GTru0"})
	_, _ = c.SearchChannels(ctx, "name", 5)
	for _, resource := range []string{"channels", "playlists", "playlistItems", "videos", "search"} {
		if asked[resource] == "" {
			t.Errorf("%s was asked without fields", resource)
		}
	}
	for resource, unused := range map[string]string{"playlistItems": "description", "videos": "tags", "channels": "country"} {
		if strings.Contains(asked[resource], unused) {
			t.Errorf("%s asks for %s, which nothing reads: %s", resource, unused, asked[resource])
		}
	}
}
