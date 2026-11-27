package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

// Silo keeps one entry per IMDb ID. When the ID a lookup finds for a channel
// is held by another title that has files, as the TV show is whose official
// channel it is, Silo refuses the channel's whole refresh ("refusing to merge
// matched items"). The channel's entry then still has no IMDb ID, so the next
// refresh would be offered the same ID and fail the same way, for as long as
// the lookup is on.
//
// So the plugin records each ID it fills in, in the state Silo keeps for a
// plugin installation, which outlasts the plugin's process (Silo stops an
// idle plugin after 30 minutes). A refresh that arrives without the ID, some
// time after it was offered, shows that Silo did not keep it: another title
// holds it, or an administrator removed it. Either way it is not offered to
// that series again.

// offersStateKey is the key of the record in the installation's state.
const offersStateKey = "imdb-offers"

// offerSettle is how long after an ID is first offered a refresh without it
// still counts as the same attempt, and gets the ID again. Silo stores a
// refresh's IDs seconds after it asks for them; the wait keeps a second
// request made meanwhile from being read as a refusal.
const offerSettle = 5 * time.Minute

// maxOffers bounds the record: Silo allows a state value 256 KiB, and an
// entry is about 90 bytes. Past it the oldest entries go.
const maxOffers = 1500

// offer is an IMDb ID a lookup found for a series.
type offer struct {
	IMDb string `json:"imdb"`
	// At is when the ID was first offered.
	At time.Time `json:"at"`
	// Dropped is set once Silo has asked again without the ID.
	Dropped bool `json:"dropped,omitempty"`
}

// offers is the record, by the series' YouTube ID.
type offers struct {
	Series map[string]offer `json:"series"`
}

// stateStore is the part of Silo's plugin state the record needs: a read and
// a write that no other process's write can come between.
type stateStore interface {
	UpdateInstanceState(ctx context.Context, key string, update func(current []byte, found bool) ([]byte, error)) error
}

// hostState is this installation's state in Silo, nil while the plugin has
// no connection to it. Replaced by tests.
var hostState = func() stateStore {
	if host := runtime.Host(); host != nil {
		return host
	}
	return nil
}

// offerClock is replaced by tests.
var offerClock = time.Now

var (
	errNoHostState = errors.New("no connection to Silo's plugin state")
	// errRecordStands stops an update that has nothing to write.
	errRecordStands = errors.New("the record stands")
)

// offerIMDb reports whether imdb, which a lookup found for the series, is to
// be filled into its entry, which has no IMDb ID. notKept is true the one
// time this call finds that Silo did not keep the ID. Without the record
// nothing is offered: an ID offered and not recorded could fail every
// refresh of the series.
func offerIMDb(ctx context.Context, seriesID, imdb string) (fill, notKept bool, first time.Time, err error) {
	store := hostState()
	if store == nil {
		return false, false, time.Time{}, errNoHostState
	}
	now := offerClock()
	err = store.UpdateInstanceState(ctx, offersStateKey, func(current []byte, _ bool) ([]byte, error) {
		// The update can run again after another process's write: every
		// result is set afresh.
		fill, notKept, first = false, false, time.Time{}
		record := offers{}
		// A record that cannot be read is started again: the cost is one
		// refused refresh for a series whose ID Silo does not keep.
		_ = json.Unmarshal(current, &record)
		if record.Series == nil {
			record.Series = make(map[string]offer)
		}
		known, ok := record.Series[seriesID]
		switch {
		case !ok || known.IMDb != imdb:
			known = offer{IMDb: imdb, At: now}
			fill = true
		case known.Dropped:
			return nil, errRecordStands
		case now.Sub(known.At) < offerSettle:
			fill = true
			return nil, errRecordStands
		default:
			known.Dropped = true
			notKept, first = true, known.At
		}
		record.Series[seriesID] = known
		record.trim()
		return json.Marshal(record)
	})
	if err != nil && !errors.Is(err, errRecordStands) {
		return false, false, time.Time{}, err
	}
	return fill, notKept, first, nil
}

// trim drops the oldest entries past maxOffers.
func (o *offers) trim() {
	if len(o.Series) <= maxOffers {
		return
	}
	ids := make([]string, 0, len(o.Series))
	for id := range o.Series {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return o.Series[a].At.Compare(o.Series[b].At) })
	for _, id := range ids[:len(ids)-maxOffers] {
		delete(o.Series, id)
	}
}
