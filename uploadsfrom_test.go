package main

import (
	"context"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestUploadsFromSettings(t *testing.T) {
	from, problems := settings{
		uploadsFromYear: " 2022 ",
		uploadsFromByID: "UCuk26DMu7ewAkC-ntx7esiA=2024\nUC7XraaeLVbH_vrA39sO5pmA = all, PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat: 2023;\n\n UCslJxr8-nmxGaobFGGTFDbA 2021",
	}.uploadsFrom()
	if len(problems) != 0 {
		t.Fatalf("problems = %q", problems)
	}
	want := map[string]int{
		"UCuk26DMu7ewAkC-ntx7esiA":           2024,
		"UC7XraaeLVbH_vrA39sO5pmA":           0,
		"PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat": 2023,
		"UCslJxr8-nmxGaobFGGTFDbA":           2021,
	}
	if from.Year != 2022 || len(from.ByID) != len(want) {
		t.Fatalf("from = %+v", from)
	}
	for id, year := range want {
		if got, ok := from.ByID[id]; !ok || got != year {
			t.Errorf("%s = %d, %v; want %d", id, got, ok, year)
		}
	}

	for _, empty := range []string{"", "all", "ALL", "0"} {
		if from, problems := (settings{uploadsFromYear: empty}).uploadsFrom(); from.Year != 0 || from.ByID != nil || len(problems) != 0 {
			t.Errorf("%q: from = %+v, problems = %q; want every upload", empty, from, problems)
		}
	}

	// What cannot be read is named and left out; the rest stands.
	from, problems = settings{
		uploadsFromYear: "last year",
		uploadsFromByID: "UCuk26DMu7ewAkC-ntx7esiA=2024\nMega Machines=2020\nUC7XraaeLVbH_vrA39sO5pmA=1999\njust-some-words",
	}.uploadsFrom()
	if from.Year != 0 || len(from.ByID) != 1 || from.ByID["UCuk26DMu7ewAkC-ntx7esiA"] != 2024 {
		t.Errorf("from = %+v, want the one good entry", from)
	}
	if len(problems) != 4 {
		t.Errorf("%d problems, want 4: %q", len(problems), problems)
	}
}

// The setting reaches the listing: uploads before the year are no episodes,
// and Check Connection says what it cannot read.
func TestUploadsFromThroughTheHandlers(t *testing.T) {
	fakeAPI(t)
	ids, _ := structpb.NewStruct(map[string]any{"youtube": "UCA94TZluH5eQtkQpaCZQfRw"})
	episodes := func(config map[string]any) int {
		t.Helper()
		resp, err := configuredWith(t, config).GetEpisodes(context.Background(), &pluginv1.GetEpisodesRequest{ProviderIds: ids, SeasonNumber: 2024})
		if err != nil {
			t.Fatal(err)
		}
		return len(resp.GetEpisodes())
	}
	if got := episodes(map[string]any{"api_key": "k"}); got != 1 {
		t.Fatalf("every upload: %d episodes in 2024, want 1", got)
	}
	if got := episodes(map[string]any{"api_key": "k", "uploads_from": "2025"}); got != 0 {
		t.Errorf("from 2025: %d episodes in 2024, want none", got)
	}
	if got := episodes(map[string]any{"api_key": "k", "uploads_from": "2025", "uploads_from_by_id": "UCA94TZluH5eQtkQpaCZQfRw=all"}); got != 1 {
		t.Errorf("from 2025, but this channel whole: %d episodes in 2024, want 1", got)
	}
	// A year a number field sent as a number.
	if got := episodes(map[string]any{"api_key": "k", "uploads_from": 2025.0}); got != 0 {
		t.Errorf("from 2025 as a number: %d episodes in 2024, want none", got)
	}

	resp := checkSettings(t, map[string]any{"api_key": "k", "uploads_from": "2023", "uploads_from_by_id": "Mega Machines=2020"})
	if resp.GetOk() || !strings.Contains(resp.GetMessage(), "Mega Machines=2020") {
		t.Errorf("an entry that names no ID: %+v", resp)
	}
}
