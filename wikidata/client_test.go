package wikidata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	channel  = "UCB9_VH_CNbbH4GfKu8qh63w"
	playlist = "PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat"
)

func bindings(ids ...string) string {
	rows := make([]string, len(ids))
	for i, id := range ids {
		rows[i] = `{"imdb":{"type":"literal","value":"` + id + `"}}`
	}
	return `{"head":{"vars":["imdb"]},"results":{"bindings":[` + strings.Join(rows, ",") + `]}}`
}

func TestIMDbTitle(t *testing.T) {
	var calls atomic.Int64
	answers := map[string]string{
		channel:                    bindings("tt4031218"),
		playlist:                   bindings("tt2649670"),
		"UCtwoShows0000000000000a": bindings("tt0072562", "tt0313343"),
		"UCpersonAndShow00000000a": bindings("nm0000001", "tt8836798", "tt8836798", "co0000001"),
		"UCnothing0000000000000aa": bindings(),
		"UCpersonOnly0000000000aa": bindings("nm0000001"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("User-Agent") != "test-agent/1" || r.URL.Query().Get("format") != "json" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		query := r.URL.Query().Get("query")
		for id, answer := range answers {
			property := "wdt:P2397"
			if id == playlist {
				property = "wdt:P4300"
			}
			if strings.Contains(query, property+` "`+id+`"`) && strings.Contains(query, "wdt:P345 ?imdb") {
				_, _ = w.Write([]byte(answer))
				return
			}
		}
		http.Error(w, "unexpected query: "+query, http.StatusBadRequest)
	}))
	defer server.Close()
	c := NewClient(server.URL, "test-agent/1", server.Client())
	ctx := context.Background()

	for id, want := range map[string]string{
		channel:                    "tt4031218",
		playlist:                   "tt2649670",
		"UCtwoShows0000000000000a": "", // a network's channel, listed by two shows
		"UCpersonAndShow00000000a": "tt8836798",
		"UCnothing0000000000000aa": "",
		"UCpersonOnly0000000000aa": "",
	} {
		got, err := c.IMDbTitle(ctx, id)
		if err != nil || got != want {
			t.Errorf("IMDbTitle(%q) = %q, %v; want %q", id, got, err, want)
		}
	}

	// Nothing but an ID is ever put into a query.
	before := calls.Load()
	for _, id := range []string{"", "short", `UC" . ?s ?p ?o . #`, "UCB9_VH_CNbbH4GfKu8qh63w\"", "has space in it"} {
		if got, err := c.IMDbTitle(ctx, id); err != nil || got != "" {
			t.Errorf("IMDbTitle(%q) = %q, %v; want nothing", id, got, err)
		}
	}
	if calls.Load() != before {
		t.Errorf("%d requests for values that are not IDs", calls.Load()-before)
	}
}

func TestIMDbTitleFailures(t *testing.T) {
	status := http.StatusTooManyRequests
	body := "slow down"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	c := NewClient(server.URL, "test-agent/1", server.Client())
	if _, err := c.IMDbTitle(context.Background(), channel); !errors.Is(err, ErrRequestFailed) {
		t.Errorf("HTTP 429: err = %v, want ErrRequestFailed", err)
	}
	status, body = http.StatusOK, "<html>not json</html>"
	if _, err := c.IMDbTitle(context.Background(), channel); !errors.Is(err, ErrRequestFailed) {
		t.Errorf("not JSON: err = %v, want ErrRequestFailed", err)
	}
	server.Close()
	if _, err := c.IMDbTitle(context.Background(), channel); !errors.Is(err, ErrRequestFailed) {
		t.Errorf("no server: err = %v, want ErrRequestFailed", err)
	}
}

func TestIsTitleID(t *testing.T) {
	for id, want := range map[string]bool{"tt0072562": true, "tt12791930": true, "tt123": false, "nm0000001": false, "TT0072562": false, "": false, "tt0072562x": false} {
		if IsTitleID(id) != want {
			t.Errorf("IsTitleID(%q) = %v", id, !want)
		}
	}
}
