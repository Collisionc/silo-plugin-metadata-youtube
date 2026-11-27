package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/collisionc/silo-plugin-metadata-youtube/youtube"
)

// capturedLog holds the plug-in's log lines for a test, and the tally's
// timer for the test to fire.
type capturedLog struct {
	buffer *bytes.Buffer
	waits  []time.Duration
	tally  func()
}

func captureLog(t *testing.T) *capturedLog {
	t.Helper()
	captured := &capturedLog{buffer: &bytes.Buffer{}}
	previousLogger, previousLookups := logger, lookups
	logger = newLogger(captured.buffer)
	lookups = newLookupLog()
	lookups.after = func(wait time.Duration, run func()) {
		captured.waits = append(captured.waits, wait)
		captured.tally = run
	}
	t.Cleanup(func() { logger, lookups = previousLogger, previousLookups })
	return captured
}

func (c *capturedLog) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(c.buffer.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("a log line is not JSON: %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

// Silo's plug-in host (hashicorp/go-plugin) passes a stderr line on at its
// own level only when it is hclog's JSON: "@level", "@message", and a
// "@timestamp" it can parse with the layout below. Any other line it logs at
// debug level, which Silo's log does not keep.
func TestLogLinesAreInTheFormSilosHostPassesOn(t *testing.T) {
	captured := captureLog(t)
	logger.Warn("youtube: test", "file_path", "/a b/c [x].mkv")
	lines := captured.lines(t)
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1: %s", len(lines), captured.buffer.String())
	}
	entry := lines[0]
	if entry["@level"] != "warn" || entry["@message"] != "youtube: test" || entry["file_path"] != "/a b/c [x].mkv" {
		t.Errorf("entry = %v", entry)
	}
	stamp, _ := entry["@timestamp"].(string)
	if _, err := time.Parse("2006-01-02T15:04:05.000000Z07:00", stamp); err != nil {
		t.Errorf("@timestamp %q is not in the layout the host parses: %v", stamp, err)
	}

	// Below info nothing is written: the host would drop it anyway.
	logger.Debug("youtube: not written")
	if got := len(captured.lines(t)); got != 1 {
		t.Errorf("lines after a debug call = %d, want 1", got)
	}
}

func TestLookupsAreSampledByShapeAndTallied(t *testing.T) {
	captured := captureLog(t)
	for range 5 {
		lookups.note("GetMetadata/episode", noVideoID, nil, "provider_id:channel ids:none file:none", "provider_id", "UCA94TZluH5eQtkQpaCZQfRw", "file_path", "")
	}
	lookups.note("GetMetadata/episode", noVideoID, nil, "provider_id:none ids:none file:names_no_video", "file_path", "/x/y.mkv")
	lookups.note("GetEpisodes", found, nil, "series:channel ids:channel season:set", "answers", 3)

	lines := captured.lines(t)
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4 (two of the first shape, one of the second, one found): %s", len(lines), captured.buffer.String())
	}
	for i, want := range []string{"warn", "warn", "warn", "info"} {
		if lines[i]["@level"] != want {
			t.Errorf("line %d level = %v, want %s", i, lines[i]["@level"], want)
		}
	}
	first := lines[0]
	if first["lookup"] != "GetMetadata/episode" || first["outcome"] != noVideoID || first["shape"] != "provider_id:channel ids:none file:none" ||
		first["provider_id"] != "UCA94TZluH5eQtkQpaCZQfRw" {
		t.Errorf("first sample = %v", first)
	}
	if lines[2]["file_path"] != "/x/y.mkv" || lines[3]["answers"] != float64(3) {
		t.Errorf("later samples = %v, %v", lines[2], lines[3])
	}

	// One tally per busy spell, five minutes after its first lookup.
	if len(captured.waits) != 1 || captured.waits[0] != tallyEvery {
		t.Fatalf("tally timers = %v, want one of %s", captured.waits, tallyEvery)
	}
	captured.tally()
	lines = captured.lines(t)
	last := lines[len(lines)-1]
	if last["@level"] != "info" || last["@message"] != "youtube: what Silo asked in 5 minutes" || last["lookups"] != float64(7) ||
		last["by_kind_and_outcome"] != "GetEpisodes:found=1 GetMetadata/episode:no_video_id=6" {
		t.Errorf("tally = %v", last)
	}

	// The next lookup starts a new spell and a new count; a shape already
	// shown twice is not shown again.
	lookups.note("GetMetadata/episode", noVideoID, nil, "provider_id:channel ids:none file:none")
	if len(captured.waits) != 2 {
		t.Fatalf("tally timers after a new spell = %d, want 2", len(captured.waits))
	}
	before := len(captured.lines(t))
	captured.tally()
	lines = captured.lines(t)
	if len(lines) != before+1 || lines[len(lines)-1]["by_kind_and_outcome"] != "GetMetadata/episode:no_video_id=1" {
		t.Errorf("second tally = %v", lines[len(lines)-1])
	}
	// A spell with no lookups logs nothing.
	captured.tally()
	if got := len(captured.lines(t)); got != len(lines) {
		t.Errorf("an empty tally wrote %d line(s)", got-len(lines))
	}
}

// What a refresh sends cannot be seen in Silo: the handlers say it.
func TestHandlersLogWhatSiloSent(t *testing.T) {
	fakeAPI(t)
	s := configured(t)
	captured := captureLog(t)
	ctx := context.Background()
	series, _ := structpb.NewStruct(map[string]any{"youtube": "UCA94TZluH5eQtkQpaCZQfRw"})
	file := "/media/YouTube/Brain Break Land [UCA94TZluH5eQtkQpaCZQfRw]/2024-01-02 - Mario's Winter Run [0bNNs5GTru0].mkv"

	if meta, err := s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "episode", ProviderIds: series, Language: "en"}); err != nil || meta.GetItem() != nil {
		t.Fatalf("series ID alone: item = %+v, err = %v", meta.GetItem(), err)
	}
	if meta, err := s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "episode", ProviderIds: series, FilePath: file}); err != nil || meta.GetItem() == nil {
		t.Fatalf("with the file: item = %+v, err = %v", meta.GetItem(), err)
	}
	if meta, err := s.GetMetadata(ctx, &pluginv1.GetMetadataRequest{ItemType: "season", ProviderId: "UCA94TZluH5eQtkQpaCZQfRw/2024"}); err != nil || meta.GetItem() != nil {
		t.Fatalf("season: item = %+v, err = %v", meta.GetItem(), err)
	}
	if _, err := s.GetEpisodes(ctx, &pluginv1.GetEpisodesRequest{SeriesProviderId: "UCA94TZluH5eQtkQpaCZQfRw", SeasonNumber: 2024}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEpisodes(ctx, &pluginv1.GetEpisodesRequest{SeasonNumber: 2024}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(ctx, &pluginv1.SearchMetadataRequest{Query: "anything", ItemType: "book"}); err != nil {
		t.Fatal(err)
	}

	type want struct{ level, lookup, outcome, shape string }
	wants := []want{
		{"warn", "GetMetadata/episode", noVideoID, "provider_id:none ids:channel file:none"},
		{"info", "GetMetadata/episode", found, "provider_id:none ids:channel file:names_a_video"},
		{"warn", "GetMetadata/season", noSeasons, "provider_id:season ids:none file:none"},
		{"info", "GetEpisodes", found, "series:channel ids:none season:set"},
		{"warn", "GetEpisodes", noID, "series:none ids:none season:set"},
		{"info", "Search/book", notAnswered, "query:set ids:none"},
	}
	lines := captured.lines(t)
	if len(lines) != len(wants) {
		t.Fatalf("lines = %d, want %d: %s", len(lines), len(wants), captured.buffer.String())
	}
	for i, w := range wants {
		got := lines[i]
		if got["@level"] != w.level || got["lookup"] != w.lookup || got["outcome"] != w.outcome || got["shape"] != w.shape {
			t.Errorf("line %d = %v, want %+v", i, got, w)
		}
	}
	if lines[0]["provider_ids"] != "youtube=UCA94TZluH5eQtkQpaCZQfRw" || lines[0]["file_path"] != "" || lines[0]["language"] != "en" {
		t.Errorf("the lookup that named no video = %v", lines[0])
	}
	if lines[1]["file_path"] != file {
		t.Errorf("the lookup that found its video = %v", lines[1])
	}
	if lines[3]["answers"] != float64(1) || lines[3]["season_number"] != float64(2024) {
		t.Errorf("the episode list = %v", lines[3])
	}
}

// A lookup that fails says why, in its sample and in the tally, by the kind
// of failure Silo is answered with.
func TestFailedLookupsSayWhy(t *testing.T) {
	log := captureLog(t)
	quota := &youtube.APIError{Status: 403, Reason: "quotaExceeded", Message: "You have exceeded your quota."}
	for range 3 {
		lookups.note("GetEpisodes", outcomeOf(true, 0, quota), quota, "series:channel ids:channel season:set", "season_number", 1)
	}
	down := fmt.Errorf("%w: dial tcp: no such host", youtube.ErrRequestFailed)
	lookups.note("GetEpisodes", outcomeOf(true, 0, down), down, "series:channel ids:channel season:set", "season_number", 1)
	log.tally()

	var samples, tallies []map[string]any
	for _, line := range log.lines(t) {
		switch line["@message"] {
		case "youtube: a lookup that failed, as Silo sent it":
			samples = append(samples, line)
		case "youtube: what Silo asked in 5 minutes":
			tallies = append(tallies, line)
		}
	}
	// Two of the three quota failures, and the outage: a kind of its own.
	if len(samples) != 3 {
		t.Fatalf("%d samples, want 3: %v", len(samples), samples)
	}
	if got := samples[0]; got["@level"] != "warn" || got["code"] != "ResourceExhausted" || got["outcome"] != "error(ResourceExhausted)" ||
		!strings.Contains(got["error"].(string), "exceeded your quota") {
		t.Errorf("the quota sample = %v", got)
	}
	if got := samples[2]; got["code"] != "Unavailable" || !strings.Contains(got["error"].(string), "no such host") {
		t.Errorf("the outage sample = %v", got)
	}
	if len(tallies) != 1 || tallies[0]["by_kind_and_outcome"] != "GetEpisodes:error(ResourceExhausted)=3 GetEpisodes:error(Unavailable)=1" {
		t.Errorf("tallies = %v", tallies)
	}
}
