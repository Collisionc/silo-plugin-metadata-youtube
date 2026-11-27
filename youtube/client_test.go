package youtube

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
