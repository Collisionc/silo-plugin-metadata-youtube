package omdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeOMDb answers a search by the name searched for.
type fakeOMDb struct {
	calls   atomic.Int64
	status  int
	answers map[string]string
}

func (f *fakeOMDb) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	q := r.URL.Query()
	if q.Get("apikey") != "k" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"Response":"False","Error":"Invalid API key!"}`))
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if q.Get("i") != "" {
		_, _ = w.Write([]byte(`{"Title":"The Matrix","Response":"True"}`))
		return
	}
	if q.Get("type") != "series" {
		http.Error(w, "the search must ask for series", http.StatusBadRequest)
		return
	}
	if answer, ok := f.answers[q.Get("s")]; ok {
		_, _ = w.Write([]byte(answer))
		return
	}
	_, _ = w.Write([]byte(`{"Response":"False","Error":"Series not found!"}`))
}

func newFake(t *testing.T) (*Client, *fakeOMDb) {
	t.Helper()
	fake := &fakeOMDb{answers: map[string]string{
		"DEATH BATTLE!": `{"Search":[
			{"Title":"Death Battle","Year":"2010–","imdbID":"tt4031218","Type":"series"},
			{"Title":"Death Battle Cast","Year":"2015–","imdbID":"tt5000001","Type":"series"},
			{"Title":"Death Battle","Year":"2010","imdbID":"tt5000002","Type":"movie"}],"totalResults":"3","Response":"True"}`,
		"Friends": `{"Search":[
			{"Title":"Friends","Year":"1994–2004","imdbID":"tt0108778","Type":"series"},
			{"Title":"Friends","Year":"2021–","imdbID":"tt5000003","Type":"series"},
			{"Title":"Friends","Year":"2021–","imdbID":"TT5000003","Type":"series"},
			{"Title":"Friends","Year":"N/A","imdbID":"tt5000004","Type":"series"},
			{"Title":"Friends","Year":"2019–","imdbID":"nm0000001","Type":"series"}],"totalResults":"5","Response":"True"}`,
		"It": `{"Response":"False","Error":"Too many results."}`,
	}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return NewClient(server.URL, "k", server.Client()), fake
}

func ids(series []Series) string {
	out := make([]string, len(series))
	for i, s := range series {
		out[i] = s.IMDbID
	}
	return strings.Join(out, ",")
}

func TestSeriesNamed(t *testing.T) {
	c, fake := newFake(t)
	ctx := context.Background()

	// The name is compared by its letters and digits; a longer title and a
	// film of the same name are not the series.
	series, err := c.SeriesNamed(ctx, "DEATH BATTLE!", 0)
	if err != nil || len(series) != 1 || series[0] != (Series{IMDbID: "tt4031218", Title: "Death Battle", Year: 2010}) {
		t.Errorf("exact name: %+v, %v", series, err)
	}
	// Several series share a name: all are returned, once each, for the
	// caller to refuse. A year floor leaves out the older ones and those
	// without a year.
	if series, err = c.SeriesNamed(ctx, "Friends", 0); err != nil || ids(series) != "tt0108778,tt5000003,tt5000004" {
		t.Errorf("shared name: %s, %v", ids(series), err)
	}
	if series, err = c.SeriesNamed(ctx, "Friends", 2015); err != nil || ids(series) != "tt5000003" {
		t.Errorf("shared name from 2015: %s, %v", ids(series), err)
	}
	// Nothing found, and a name too common to search for.
	for _, name := range []string{"Nobody Here", "It"} {
		if series, err = c.SeriesNamed(ctx, name, 0); err != nil || len(series) != 0 {
			t.Errorf("%q: %+v, %v; want nothing", name, series, err)
		}
	}
	// A name without two letters or digits is not searched for.
	before := fake.calls.Load()
	for _, name := range []string{"", "  ", "!!!", "a", "★"} {
		if series, err = c.SeriesNamed(ctx, name, 0); err != nil || len(series) != 0 {
			t.Errorf("%q: %+v, %v; want nothing", name, series, err)
		}
	}
	if fake.calls.Load() != before {
		t.Errorf("%d requests for names that say nothing", fake.calls.Load()-before)
	}
}

func TestKeyAndFailures(t *testing.T) {
	c, fake := newFake(t)
	ctx := context.Background()
	if err := c.CheckKey(ctx); err != nil {
		t.Errorf("a good key: %v", err)
	}
	bad := NewClient(c.baseURL, "wrong", c.http)
	if err := bad.CheckKey(ctx); !errors.Is(err, ErrKeyRefused) {
		t.Errorf("a bad key: %v, want ErrKeyRefused", err)
	}
	if _, err := bad.SeriesNamed(ctx, "Friends", 0); !errors.Is(err, ErrKeyRefused) {
		t.Errorf("a bad key searching: %v, want ErrKeyRefused", err)
	}
	if _, err := NewClient(c.baseURL, "", c.http).SeriesNamed(ctx, "Friends", 0); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("no key: %v, want ErrNoAPIKey", err)
	}
	fake.answers["Limit"] = `{"Response":"False","Error":"Request limit reached!"}`
	if _, err := c.SeriesNamed(ctx, "Limit", 0); !errors.Is(err, ErrKeyRefused) {
		t.Errorf("the day's requests used up: %v, want ErrKeyRefused", err)
	}
	fake.status = http.StatusBadGateway
	_, err := c.SeriesNamed(ctx, "Friends", 0)
	if !errors.Is(err, ErrRequestFailed) {
		t.Errorf("HTTP 502: %v, want ErrRequestFailed", err)
	}
	// The key is part of the URL and must not reach an error.
	gone := NewClient("http://127.0.0.1:1/", "secret-key", nil)
	if _, err = gone.SeriesNamed(ctx, "Friends", 0); !errors.Is(err, ErrRequestFailed) || strings.Contains(err.Error(), "secret-key") {
		t.Errorf("no server: %v", err)
	}
}
