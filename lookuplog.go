package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/collisionc/silo-plugin-metadata-youtube/youtube"
)

// logger writes hclog's JSON lines on stderr. Silo's plug-in host reads a
// plug-in's stderr and passes such a line on at the level it names; a line in
// any other form it shows at debug level only, which Silo's log does not
// keep: the warnings this plug-in used to write with slog were never seen
// there.
var logger = newLogger(os.Stderr)

func newLogger(out io.Writer) hclog.Logger {
	return hclog.New(&hclog.LoggerOptions{Output: out, JSONFormat: true, Level: hclog.Info})
}

// What a lookup came to.
const (
	found        = "found"
	nothingFound = "nothing_found"
	noID         = "no_id"
	noVideoID    = "no_video_id"
	noSeasons    = "seasons_not_looked_up"
	notAnswered  = "not_answered" // left to other providers, by design
	failed       = "error"
)

const (
	// tallyEvery is how long after the first lookup of a busy spell the
	// tally of that spell is logged.
	tallyEvery = 5 * time.Minute
	// samplesPerShape is how many lookups of one shape and outcome are
	// logged in full while the plug-in runs.
	samplesPerShape = 2
)

// lookupLog says in Silo's log what Silo asks this plug-in and what it gets.
// Nothing else shows it: Silo logs only "no metadata found from any provider"
// when a refresh fails, not what it sent. Each kind of lookup is counted by
// its outcome and the counts are logged once per busy spell; the first
// lookups of each shape - which IDs and file path a request carried - are
// logged in full, so that a request Silo forms differently, as a refresh
// does, is seen once without every lookup being logged.
type lookupLog struct {
	mu      sync.Mutex
	tally   map[string]int
	sampled map[string]int
	pending bool
	after   func(time.Duration, func())
}

func newLookupLog() *lookupLog {
	return &lookupLog{
		tally:   make(map[string]int),
		sampled: make(map[string]int),
		after:   func(wait time.Duration, run func()) { time.AfterFunc(wait, run) },
	}
}

var lookups = newLookupLog()

// note records one lookup: kind is the call and what it was for
// ("GetMetadata/episode"), shape what the request carried without the values
// themselves, and fields the values. A lookup that failed is counted and
// sampled by the kind of failure too (the gRPC code Silo is answered with),
// and its sample says why: Silo's own log has the failure, not what was
// asked, and this one had the question without the reason.
func (l *lookupLog) note(kind, outcome string, err error, shape string, fields ...any) {
	if err != nil {
		code := statusCode(err).String()
		outcome += "(" + code + ")"
		fields = append(fields, "error", err.Error(), "code", code)
	}
	l.mu.Lock()
	l.tally[kind+":"+outcome]++
	key := kind + ":" + outcome + " " + shape
	sample := l.sampled[key] < samplesPerShape
	if sample {
		l.sampled[key]++
	}
	schedule := !l.pending
	l.pending = true
	l.mu.Unlock()
	if schedule {
		l.after(tallyEvery, l.logTally)
	}
	if !sample {
		return
	}
	args := append([]any{"lookup", kind, "outcome", outcome, "shape", shape}, fields...)
	switch {
	case err != nil:
		logger.Warn("youtube: a lookup that failed, as Silo sent it", args...)
	case outcome == found, outcome == notAnswered:
		logger.Info("youtube: a lookup as Silo sent it", args...)
	default:
		logger.Warn("youtube: a lookup that found nothing, as Silo sent it", args...)
	}
}

// logTally logs the lookups counted since the last tally, by kind and
// outcome, and starts a new count.
func (l *lookupLog) logTally() {
	l.mu.Lock()
	tally := l.tally
	l.tally = make(map[string]int)
	l.pending = false
	l.mu.Unlock()
	keys := make([]string, 0, len(tally))
	total := 0
	for key, count := range tally {
		keys = append(keys, key)
		total += count
	}
	if total == 0 {
		return
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, tally[key]))
	}
	logger.Info("youtube: what Silo asked in 5 minutes", "lookups", total, "by_kind_and_outcome", strings.Join(parts, " "))
}

// outcomeOf names what a lookup came to from what it was asked about and how
// many answers it gave.
func outcomeOf(asked bool, answers int, err error) string {
	switch {
	case err != nil:
		return failed
	case !asked:
		return noID
	case answers == 0:
		return nothingFound
	}
	return found
}

// idShape says what kind of ID a lookup carried, without the ID.
func idShape(id string) string {
	id = strings.TrimSpace(id)
	switch {
	case id == "":
		return "none"
	case strings.Contains(id, "/"):
		return "season"
	case youtube.IsVideoID(id):
		return "video"
	case youtube.IsChannelID(id):
		return "channel"
	}
	return "other"
}

// pathShape says whether a lookup carried a file path and whether the file's
// name holds a video's ID.
func pathShape(path string) string {
	if strings.TrimSpace(path) == "" {
		return "none"
	}
	if _, ok := youtube.VideoIDFromPath(path); ok {
		return "names_a_video"
	}
	return "names_no_video"
}

// idsText writes a lookup's provider IDs as "key=value" pairs in key order.
func idsText(ids *structpb.Struct) string {
	values := stringMap(ids)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, " ")
}
