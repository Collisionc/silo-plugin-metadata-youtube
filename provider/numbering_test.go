package provider

import (
	"testing"
	"time"
)

func at(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNumberYearSeasonsAndDailyCounter(t *testing.T) {
	uploads := []Upload{
		{VideoID: "late", Published: at("2024-01-02T20:00:00Z")},
		{VideoID: "early", Published: at("2024-01-02T13:00:01Z")},
		{VideoID: "dec", Published: at("2023-12-31T09:00:00Z")},
		{VideoID: "nodate"},
		{VideoID: "early", Published: at("2024-01-02T13:00:01Z")}, // listed twice
		{VideoID: "oct", Published: at("2025-10-21T14:30:00Z")},
	}
	got := Number(uploads, time.UTC, SeasonsByYear)
	want := []struct {
		id      string
		season  int
		episode int
		date    string
	}{
		{"dec", 2023, 123101, "2023-12-31"},
		{"early", 2024, 10201, "2024-01-02"},
		{"late", 2024, 10202, "2024-01-02"},
		{"oct", 2025, 102101, "2025-10-21"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d uploads, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.VideoID != w.id || g.Season != w.season || g.Episode != w.episode || g.AirDate != w.date {
			t.Errorf("upload %d = %s S%dE%d %s; want %s S%dE%d %s", i, g.VideoID, g.Season, g.Episode, g.AirDate, w.id, w.season, w.episode, w.date)
		}
	}
}

// A new upload must not renumber the episodes before it: Silo keeps watch
// state on its episodes.
func TestNumberIsStableAsTheChannelGrows(t *testing.T) {
	before := Number([]Upload{
		{VideoID: "a", Published: at("2024-03-01T10:00:00Z")},
		{VideoID: "b", Published: at("2024-03-05T10:00:00Z")},
	}, time.UTC, SeasonsByYear)
	after := Number([]Upload{
		{VideoID: "a", Published: at("2024-03-01T10:00:00Z")},
		{VideoID: "b", Published: at("2024-03-05T10:00:00Z")},
		{VideoID: "c", Published: at("2024-03-05T18:00:00Z")},
		{VideoID: "d", Published: at("2024-04-01T10:00:00Z")},
	}, time.UTC, SeasonsByYear)
	for i := range before {
		if before[i].Episode != after[i].Episode || before[i].VideoID != after[i].VideoID {
			t.Errorf("episode %s moved from E%d to %s E%d", before[i].VideoID, before[i].Episode, after[i].VideoID, after[i].Episode)
		}
	}
	if after[2].Episode != 30502 {
		t.Errorf("second upload of March 5th = E%d, want E30502", after[2].Episode)
	}
}

// The air date follows the configured zone, so it matches a downloader that
// dates files in local time.
func TestNumberUsesTheConfiguredTimeZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	upload := []Upload{{VideoID: "v", Published: at("2024-01-01T03:00:00Z")}}
	if got := Number(upload, time.UTC, SeasonsByYear)[0]; got.AirDate != "2024-01-01" || got.Season != 2024 {
		t.Errorf("UTC: %+v", got)
	}
	if got := Number(upload, ny, SeasonsByYear)[0]; got.AirDate != "2023-12-31" || got.Season != 2023 || got.Episode != 123101 {
		t.Errorf("New York: %+v", got)
	}
}

// One-season mode: every upload in season 1, the year leading the number.
func TestNumberSingleSeason(t *testing.T) {
	got := Number([]Upload{
		{VideoID: "late", Published: at("2024-01-02T20:00:00Z")},
		{VideoID: "early", Published: at("2024-01-02T13:00:01Z")},
		{VideoID: "dec", Published: at("2023-12-31T09:00:00Z")},
		{VideoID: "far", Published: at("2147-12-31T09:00:00Z")},
	}, time.UTC, SingleSeason)
	want := map[string]int{"dec": 2023123101, "early": 2024010201, "late": 2024010202, "far": 2147123101}
	for _, upload := range got {
		if upload.Season != 1 || upload.Episode != want[upload.VideoID] {
			t.Errorf("%s = S%dE%d, want S1E%d", upload.VideoID, upload.Season, upload.Episode, want[upload.VideoID])
		}
		if upload.Episode > 2147483647 {
			t.Errorf("%s: E%d does not fit a 32-bit episode number", upload.VideoID, upload.Episode)
		}
	}
	if got[0].VideoID != "dec" || got[1].AirDate != "2024-01-02" {
		t.Errorf("order or dates changed: %+v", got)
	}
	if ParseSeasonMode("single") != SingleSeason || ParseSeasonMode("") != SeasonsByYear || ParseSeasonMode("bogus") != SeasonsByYear {
		t.Error("ParseSeasonMode")
	}
}
