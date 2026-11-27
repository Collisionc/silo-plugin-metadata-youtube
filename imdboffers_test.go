package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memoryState stands in for the state Silo keeps for a plugin installation.
type memoryState struct {
	mu     sync.Mutex
	values map[string][]byte
	writes int
	// down, when set, fails every call.
	down error
}

func (m *memoryState) UpdateInstanceState(_ context.Context, key string, update func([]byte, bool) ([]byte, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down != nil {
		return m.down
	}
	current, found := m.values[key]
	next, err := update(current, found)
	if err != nil {
		return err
	}
	m.values[key] = next
	m.writes++
	return nil
}

func (m *memoryState) offers(t *testing.T) map[string]offer {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var record offers
	if raw, ok := m.values[offersStateKey]; ok {
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatalf("the record is not JSON: %v", err)
		}
	}
	return record.Series
}

// fakeState gives the test an installation's state of its own, as Silo would
// keep it across the plugin's restarts.
func fakeState(t *testing.T) *memoryState {
	t.Helper()
	state := &memoryState{values: make(map[string][]byte)}
	previous := hostState
	hostState = func() stateStore { return state }
	t.Cleanup(func() { hostState = previous })
	return state
}

const brainBreakLand = "UCA94TZluH5eQtkQpaCZQfRw"

// Silo refuses the refresh of a channel whose IMDb ID another title holds,
// and asks again without the ID. The plugin offers it once more while the
// first attempt may still be under way, then never again, whichever process
// of the plugin is asked.
func TestIMDbIDSiloDidNotKeepIsNotFilledInAgain(t *testing.T) {
	fakeAPI(t)
	state := fakeState(t)
	log := captureLog(t)
	var answer atomic.Value
	answer.Store("tt0259128")
	wd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"results":{"bindings":[{"imdb":{"value":%q}}]}}`, answer.Load())
	}))
	t.Cleanup(wd.Close)
	previous := wikidataEndpoint
	wikidataEndpoint = wd.URL
	t.Cleanup(func() { wikidataEndpoint = previous })
	now := time.Date(2026, 11, 27, 17, 55, 0, 0, time.UTC)
	previousClock := offerClock
	offerClock = func() time.Time { return now }
	t.Cleanup(func() { offerClock = previousClock })
	on := map[string]any{"api_key": "k", "imdb": "wikidata"}
	// Each refresh is asked of a plugin started afresh: only Silo's state
	// carries over.
	refresh := func() any { return seriesIMDb(t, configuredWith(t, on)) }

	if got := refresh(); got != "tt0259128" {
		t.Fatalf("first refresh: imdb = %v, want tt0259128", got)
	}
	now = now.Add(offerSettle - time.Second)
	if got := refresh(); got != "tt0259128" || state.writes != 1 {
		t.Errorf("a refresh hard on the first: imdb = %v after %d writes, want the ID again and one write", got, state.writes)
	}
	now = now.Add(time.Minute)
	if got := refresh(); got != nil {
		t.Errorf("Silo asked again without the ID: imdb = %v, want none", got)
	}
	if known := state.offers(t)[brainBreakLand]; !known.Dropped || known.IMDb != "tt0259128" {
		t.Errorf("the record = %+v, want tt0259128 dropped", known)
	}
	now = now.Add(90 * 24 * time.Hour)
	if got := refresh(); got != nil || state.writes != 2 {
		t.Errorf("months later: imdb = %v after %d writes, want none and no new write", got, state.writes)
	}
	said := 0
	for _, line := range log.lines(t) {
		if message, _ := line["@message"].(string); strings.Contains(message, "Silo did not keep the IMDb ID") {
			said++
			if line["@level"] != "warn" || line["imdb"] != "tt0259128" || line["series"] != brainBreakLand {
				t.Errorf("the warning = %v", line)
			}
		}
	}
	if said != 1 {
		t.Errorf("%d warnings that Silo did not keep the ID, want one", said)
	}

	// Another ID for the series is another offer.
	answer.Store("tt4031218")
	if got := refresh(); got != "tt4031218" {
		t.Errorf("a different ID: imdb = %v, want tt4031218", got)
	}
}

// With no record of what was offered, nothing is: the series comes back
// without an IMDb ID.
func TestIMDbIDIsNotFilledInWithoutSilosState(t *testing.T) {
	fakeAPI(t)
	wd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":{"bindings":[{"imdb":{"value":"tt4031218"}}]}}`))
	}))
	t.Cleanup(wd.Close)
	previous := wikidataEndpoint
	wikidataEndpoint = wd.URL
	t.Cleanup(func() { wikidataEndpoint = previous })
	on := map[string]any{"api_key": "k", "imdb": "wikidata"}

	state := fakeState(t)
	state.down = errors.New("state is out of reach")
	if got := seriesIMDb(t, configuredWith(t, on)); got != nil {
		t.Errorf("state out of reach: imdb = %v, want none", got)
	}
	hostState = func() stateStore { return nil }
	if got := seriesIMDb(t, configuredWith(t, on)); got != nil {
		t.Errorf("no connection to Silo: imdb = %v, want none", got)
	}
}

func TestOffersRecordIsBounded(t *testing.T) {
	record := offers{Series: make(map[string]offer)}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range maxOffers + 10 {
		record.Series[fmt.Sprintf("UC%022d", i)] = offer{IMDb: "tt0000001", At: start.Add(time.Duration(i) * time.Hour)}
	}
	record.trim()
	if len(record.Series) != maxOffers {
		t.Fatalf("%d entries, want %d", len(record.Series), maxOffers)
	}
	if _, kept := record.Series[fmt.Sprintf("UC%022d", 9)]; kept {
		t.Error("an oldest entry was kept")
	}
	if _, kept := record.Series[fmt.Sprintf("UC%022d", 10)]; !kept {
		t.Error("a newer entry was dropped")
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 256<<10 {
		t.Errorf("a full record is %d bytes (err %v), over Silo's 256 KiB", len(raw), err)
	}
}
