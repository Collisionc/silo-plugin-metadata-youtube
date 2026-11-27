package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/collisionc/silo-plugin-metadata-youtube/provider"
	"github.com/collisionc/silo-plugin-metadata-youtube/youtube"
)

// Silo retries a transient failure and parks a folder after three it takes
// as permanent, so a spent quota, an outage or a listing still running must
// arrive as a transient code.
func TestStatusCodesSiloRetriesBy(t *testing.T) {
	cases := map[string]struct {
		err  error
		want codes.Code
	}{
		"quota":         {&youtube.APIError{Status: 403, Reason: "quotaExceeded"}, codes.ResourceExhausted},
		"rate limit":    {&youtube.APIError{Status: 429}, codes.ResourceExhausted},
		"outage":        {&youtube.APIError{Status: 503}, codes.Unavailable},
		"no answer":     {fmt.Errorf("%w: dial tcp: no such host", youtube.ErrRequestFailed), codes.Unavailable},
		"not JSON":      {fmt.Errorf("%w: decoding response: unexpected <", youtube.ErrRequestFailed), codes.Unavailable},
		"still listing": {fmt.Errorf("%w (listing)", provider.ErrStillWorking), codes.Unavailable},
		"no key":        {youtube.ErrNoAPIKey, codes.FailedPrecondition},
		"bad key":       {&youtube.APIError{Status: 400, Message: "API key not valid."}, codes.FailedPrecondition},
		"not found":     {&youtube.APIError{Status: 404, Message: "Playlist not found"}, codes.NotFound},
		"deadline":      {context.DeadlineExceeded, codes.DeadlineExceeded},
	}
	for name, tc := range cases {
		err := statusError(tc.err)
		if got := status.Code(err); got != tc.want {
			t.Errorf("%s: code = %v, want %v", name, got, tc.want)
		}
		if !strings.Contains(err.Error(), tc.err.Error()) {
			t.Errorf("%s: message %q lost the error's text", name, err.Error())
		}
	}
	if statusError(nil) != nil {
		t.Error("nil must stay nil")
	}
}

func TestQuotaReachesSiloAsResourceExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"You have exceeded your quota.","errors":[{"reason":"quotaExceeded"}]}}`))
	}))
	t.Cleanup(server.Close)
	previous := youtubeBaseURL
	youtubeBaseURL = server.URL
	t.Cleanup(func() { youtubeBaseURL = previous })

	ids, _ := structpb.NewStruct(map[string]any{"youtube": "UCA94TZluH5eQtkQpaCZQfRw"})
	_, err := configured(t).GetMetadata(context.Background(), &pluginv1.GetMetadataRequest{ItemType: "series", ProviderIds: ids})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("err = %v, want ResourceExhausted", err)
	}
}

// Episode numbers such as S2024E10201 mean nothing to a viewer; the series
// asks Silo's clients to show air dates instead.
func TestSeriesAsksForAirDateLabels(t *testing.T) {
	fakeAPI(t)
	ids, _ := structpb.NewStruct(map[string]any{"youtube": "UCA94TZluH5eQtkQpaCZQfRw"})
	meta, err := configured(t).GetMetadata(context.Background(), &pluginv1.GetMetadataRequest{ItemType: "series", ProviderIds: ids})
	if err != nil {
		t.Fatal(err)
	}
	if got := meta.GetItem().GetMetadata().GetFields()["episode_labels"].GetStringValue(); got != "air_date" {
		t.Errorf("episode_labels = %q, want air_date", got)
	}
}

func TestManifestDeclaresTheSettingsCheck(t *testing.T) {
	m, err := publicmanifest.Load(manifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !publicmanifest.SupportsConfigurationCheck(m) {
		t.Error("the manifest must declare configuration_check, or Silo never calls TestConfiguration")
	}
}

func checkSettings(t *testing.T, config map[string]any) *pluginv1.TestConfigurationResponse {
	t.Helper()
	value, err := structpb.NewStruct(config)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := configurationChecks{}.TestConfiguration(context.Background(),
		&pluginv1.TestConfigurationRequest{Config: []*pluginv1.ConfigEntry{{Key: configKey, Value: value}}})
	if err != nil {
		t.Fatalf("a failed check is ok: false, not an error: %v", err)
	}
	return resp
}

func TestSettingsCheck(t *testing.T) {
	var answer atomic.Value
	answer.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/channels" || r.URL.Query().Get("part") != "id" {
			t.Errorf("check asked %s?%s; want the 1-unit channel lookup", r.URL.Path, r.URL.RawQuery)
		}
		if answer.Load().(string) == "" {
			_, _ = w.Write([]byte(`{"items":[{"id":"UCBR8-60-B28hp2BmDPdntcQ"}]}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(answer.Load().(string)))
	}))
	t.Cleanup(server.Close)
	previous := youtubeBaseURL
	youtubeBaseURL = server.URL
	t.Cleanup(func() { youtubeBaseURL = previous })

	if resp := checkSettings(t, map[string]any{"api_key": "k", "timezone": "Europe/Berlin"}); !resp.GetOk() {
		t.Errorf("working key: %+v", resp)
	}
	if resp := checkSettings(t, map[string]any{}); resp.GetOk() || !strings.Contains(resp.GetMessage(), "key") {
		t.Errorf("no key: %+v", resp)
	}
	if resp := checkSettings(t, map[string]any{"api_key": "k", "timezone": "Mars/Olympus"}); resp.GetOk() || !strings.Contains(resp.GetMessage(), "Mars/Olympus") {
		t.Errorf("bad time zone: %+v", resp)
	}
	for want, body := range map[string]string{
		"quota":       `{"error":{"code":403,"message":"You have exceeded your quota.","errors":[{"reason":"quotaExceeded"}]}}`,
		"not enabled": `{"error":{"code":403,"message":"YouTube Data API v3 has not been used in project 1.","errors":[{"reason":"accessNotConfigured"}],"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED"}]}}`,
		"rejected":    `{"error":{"code":403,"message":"Requests from this IP are blocked.","errors":[{"reason":"forbidden"}],"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_IP_ADDRESS_BLOCKED"}]}}`,
	} {
		answer.Store(body)
		if resp := checkSettings(t, map[string]any{"api_key": "k"}); resp.GetOk() || !strings.Contains(resp.GetMessage(), want) {
			t.Errorf("%s: %+v", want, resp)
		}
	}
}
