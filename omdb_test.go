package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeOMDb answers every series search with search, a JSON list of results,
// for the key "ok", and counts the searches.
func fakeOMDb(t *testing.T, search *atomic.Value) *atomic.Int64 {
	t.Helper()
	var searches atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("apikey") != "ok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"Response":"False","Error":"Invalid API key!"}`))
			return
		}
		if q.Get("i") != "" {
			_, _ = w.Write([]byte(`{"Title":"The Matrix","Response":"True"}`))
			return
		}
		searches.Add(1)
		if q.Get("s") != "Brain Break Land" || q.Get("type") != "series" {
			t.Errorf("OMDb was asked %s; want a series search for the channel's name", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"Search":` + search.Load().(string) + `,"Response":"True"}`))
	}))
	t.Cleanup(server.Close)
	previous := omdbBaseURL
	omdbBaseURL = server.URL
	t.Cleanup(func() { omdbBaseURL = previous })
	return &searches
}

// The fake channel is "Brain Break Land", created in 2021.
const (
	sameName   = `[{"Title":"Brain Break Land","Year":"2021–","imdbID":"tt7777777","Type":"series"},{"Title":"Brain Break Land Live","Year":"2022–","imdbID":"tt7777778","Type":"series"}]`
	olderShow  = `[{"Title":"Brain Break Land","Year":"1994–2004","imdbID":"tt0000094","Type":"series"}]`
	twoOfAName = `[{"Title":"Brain Break Land","Year":"2021–","imdbID":"tt7777777","Type":"series"},{"Title":"Brain Break Land","Year":"2023–","imdbID":"tt7777779","Type":"series"}]`
)

func seriesIMDb(t *testing.T, s *metadataServer) any {
	t.Helper()
	ids, _ := structpb.NewStruct(map[string]any{"youtube": "UCA94TZluH5eQtkQpaCZQfRw"})
	meta, err := s.GetMetadata(context.Background(), &pluginv1.GetMetadataRequest{ItemType: "series", ProviderIds: ids})
	if err != nil || meta.GetItem().GetTitle() != "Brain Break Land" {
		t.Fatalf("series: item = %+v, err = %v", meta.GetItem(), err)
	}
	return meta.GetItem().GetProviderIds().AsMap()["imdb"]
}

// With IMDb IDs set to search OMDb, a channel gets the IMDb ID of the one
// series named as it is, and of none when the name is an older show's or
// several series share it.
func TestIMDbIDFromOMDbByChannelName(t *testing.T) {
	fakeAPI(t)
	var search atomic.Value
	search.Store(sameName)
	searches := fakeOMDb(t, &search)
	on := map[string]any{"api_key": "k", "imdb": "omdb", "omdb_api_key": "ok"}

	s := configuredWith(t, on)
	if got := seriesIMDb(t, s); got != "tt7777777" {
		t.Errorf("one series of the name: imdb = %v, want tt7777777", got)
	}
	// The answer is kept: a second refresh does not search again.
	if got := seriesIMDb(t, s); got != "tt7777777" || searches.Load() != 1 {
		t.Errorf("second refresh: imdb = %v after %d searches", got, searches.Load())
	}

	search.Store(olderShow)
	if got := seriesIMDb(t, configuredWith(t, on)); got != nil {
		t.Errorf("a show older than the channel: imdb = %v, want none", got)
	}
	search.Store(twoOfAName)
	if got := seriesIMDb(t, configuredWith(t, on)); got != nil {
		t.Errorf("two series of the name: imdb = %v, want none", got)
	}

	// Off, or without a key: OMDb is not searched.
	search.Store(sameName)
	before := searches.Load()
	for name, config := range map[string]map[string]any{
		"off":    {"api_key": "k", "omdb_api_key": "ok"},
		"no key": {"api_key": "k", "imdb": "omdb"},
	} {
		if got := seriesIMDb(t, configuredWith(t, config)); got != nil {
			t.Errorf("%s: imdb = %v, want none", name, got)
		}
	}
	if searches.Load() != before {
		t.Errorf("%d OMDb searches with the search off or without a key", searches.Load()-before)
	}

	// A refused key: the series still comes back, without the ID.
	if got := seriesIMDb(t, configuredWith(t, map[string]any{"api_key": "k", "imdb": "omdb", "omdb_api_key": "wrong"})); got != nil {
		t.Errorf("a refused key: imdb = %v, want none", got)
	}
}

// "Wikidata, then OMDb": Wikidata's answer by the channel's ID wins, and
// OMDb is searched only when Wikidata has none.
func TestIMDbIDFromWikidataThenOMDb(t *testing.T) {
	fakeAPI(t)
	var search atomic.Value
	search.Store(sameName)
	searches := fakeOMDb(t, &search)
	var wikidataAnswer atomic.Value
	wikidataAnswer.Store(`{"results":{"bindings":[{"imdb":{"value":"tt4031218"}}]}}`)
	wd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(wikidataAnswer.Load().(string)))
	}))
	t.Cleanup(wd.Close)
	previous := wikidataEndpoint
	wikidataEndpoint = wd.URL
	t.Cleanup(func() { wikidataEndpoint = previous })
	both := map[string]any{"api_key": "k", "imdb": "wikidata_omdb", "omdb_api_key": "ok"}

	if got := seriesIMDb(t, configuredWith(t, both)); got != "tt4031218" || searches.Load() != 0 {
		t.Errorf("Wikidata has it: imdb = %v after %d OMDb searches", got, searches.Load())
	}
	wikidataAnswer.Store(`{"results":{"bindings":[]}}`)
	if got := seriesIMDb(t, configuredWith(t, both)); got != "tt7777777" || searches.Load() != 1 {
		t.Errorf("Wikidata has none: imdb = %v after %d OMDb searches", got, searches.Load())
	}
}

// Check Connection tries the OMDb key too, when the settings search OMDb.
func TestSettingsCheckTriesTheOMDbKey(t *testing.T) {
	youtubeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"UCBR8-60-B28hp2BmDPdntcQ"}]}`))
	}))
	t.Cleanup(youtubeAPI.Close)
	previous := youtubeBaseURL
	youtubeBaseURL = youtubeAPI.URL
	t.Cleanup(func() { youtubeBaseURL = previous })
	var search atomic.Value
	search.Store("[]")
	fakeOMDb(t, &search)

	if resp := checkSettings(t, map[string]any{"api_key": "k", "imdb": "omdb", "omdb_api_key": "ok"}); !resp.GetOk() {
		t.Errorf("working keys: %+v", resp)
	}
	if resp := checkSettings(t, map[string]any{"api_key": "k", "imdb": "wikidata_omdb", "omdb_api_key": "wrong"}); resp.GetOk() || !strings.Contains(resp.GetMessage(), "OMDb refused") {
		t.Errorf("a refused OMDb key: %+v", resp)
	}
	if resp := checkSettings(t, map[string]any{"api_key": "k", "imdb": "omdb"}); resp.GetOk() || !strings.Contains(resp.GetMessage(), "enter an OMDb API key") {
		t.Errorf("no OMDb key: %+v", resp)
	}
	// A key that is entered but not used is not tried.
	if resp := checkSettings(t, map[string]any{"api_key": "k", "omdb_api_key": "wrong"}); !resp.GetOk() {
		t.Errorf("OMDb not searched: %+v", resp)
	}
}
