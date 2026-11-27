package youtube

import (
	"testing"
	"time"
)

func TestParseRef(t *testing.T) {
	cases := []struct {
		text string
		want Ref
		ok   bool
	}{
		// ZeroQI's Absolute Series Scanner folder tags.
		{"Brain Break Land [youtube-UCA94TZluH5eQtkQpaCZQfRw]", Ref{RefChannel, "UCA94TZluH5eQtkQpaCZQfRw"}, true},
		{"Danny Go! [youtube2-UC3wCAOfSB0W9iuKDDtNJeGw]", Ref{RefChannel, "UC3wCAOfSB0W9iuKDDtNJeGw"}, true},
		{"Errant Signal [UCm4JnxTxtvItQecKUc4zRhQ]", Ref{RefChannel, "UCm4JnxTxtvItQecKUc4zRhQ"}, true},
		{"Show {youtube-UCm4JnxTxtvItQecKUc4zRhQ}", Ref{RefChannel, "UCm4JnxTxtvItQecKUc4zRhQ"}, true},
		{"Playlist [youtube-PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat]", Ref{RefPlaylist, "PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat"}, true},
		{"Mix [PLMBYlcH3smRxxcXT7G-HHAj5czGS0sZsB]", Ref{RefPlaylist, "PLMBYlcH3smRxxcXT7G-HHAj5czGS0sZsB"}, true},
		// The shorter "yt-" prefix, in any brackets and any case.
		{"Show {yt-UCm4JnxTxtvItQecKUc4zRhQ}", Ref{RefChannel, "UCm4JnxTxtvItQecKUc4zRhQ"}, true},
		{"Show [YT-UCm4JnxTxtvItQecKUc4zRhQ]", Ref{RefChannel, "UCm4JnxTxtvItQecKUc4zRhQ"}, true},
		{"Playlist {yt-PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat}", Ref{RefPlaylist, "PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat"}, true},
		// "yt-" inside a longer ID-like word names nothing shorter.
		{"Show {yt-UCm4JnxTxtvItQecKUc4zRhQx}", Ref{}, false},
		// Pasted URLs and handles in a manual search.
		{"https://www.youtube.com/channel/UCYzPXprvl5Y-Sf0g4vX-m6g", Ref{RefChannel, "UCYzPXprvl5Y-Sf0g4vX-m6g"}, true},
		{"https://www.youtube.com/watch?v=f-wWBGo6a2w&list=PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat", Ref{RefPlaylist, "PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat"}, true},
		{"https://www.youtube.com/@dannygo", Ref{RefHandle, "dannygo"}, true},
		{"@brainbreakland", Ref{RefHandle, "brainbreakland"}, true},
		// Plain titles carry no ID: a word starting with PL or UC is not one.
		{"Brain Break Land", Ref{}, false},
		{"PLAYGROUND_ADVENTURES_FOREVER_AND_EVER", Ref{}, false},
		{"Matthew Wood (PE & Brain Breaks)", Ref{}, false},
		{"  @brainbreakland ", Ref{RefHandle, "brainbreakland"}, true},
		{"Brain Break Land [@brainbreakland]", Ref{RefHandle, "brainbreakland"}, true},
		{"Brain Break Land {@brainbreakland}", Ref{RefHandle, "brainbreakland"}, true},
		{"youtube.com/@dannygo/videos", Ref{RefHandle, "dannygo"}, true},
		// A word after "@" in a name is the name's own, not a channel.
		{"Mark Rober @home", Ref{}, false},
		{"Tom Scott plus @tomscottgo extras", Ref{}, false},
		{"Working (@home)", Ref{}, false},
		{"Live @ The Apollo", Ref{}, false},
		{"https://example.com/@dannygo", Ref{}, false},
		{"name@example.com", Ref{}, false},
	}
	for _, tc := range cases {
		got, ok := ParseRef(tc.text)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v, %v", tc.text, got, ok, tc.want, tc.ok)
		}
	}
}

func TestVideoIDFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		// TubeSync's and yt-dlp's names end in the video's ID.
		{"/media/YouTube/Brain Break Land [UCA94TZluH5eQtkQpaCZQfRw]/2024-01-02 - Mario's Winter Run [0bNNs5GTru0].mkv", "0bNNs5GTru0"},
		{`D:\YouTube\Brain Break Land\2024-01-02 - Run [0bNNs5GTru0].mp4`, "0bNNs5GTru0"},
		{"2024-01-02 - Run [youtube-0bNNs5GTru0].mkv", "0bNNs5GTru0"},
		{"2024-01-02 - Run [yt-0bNNs5GTru0].mkv", "0bNNs5GTru0"},
		// An ID can itself begin "yt-".
		{"2024-01-02 - Run [yt-abcdefgh].mkv", "yt-abcdefgh"},
		// Curly brackets, with a prefix or without.
		{"2024-01-02 - Run {0bNNs5GTru0}.mkv", "0bNNs5GTru0"},
		{"2024-01-02 - Run {youtube-0bNNs5GTru0}.mkv", "0bNNs5GTru0"},
		{"2024-01-02 - Run {yt-0bNNs5GTru0}.en.srt", "0bNNs5GTru0"},
		{"2019-05-01 - {Compilation} Best Runs {_TZW4HHkF1s}.mkv", "_TZW4HHkF1s"},
		// Round brackets are a title's own.
		{"2024-01-02 - Run (0bNNs5GTru0).mkv", ""},
		{"2024-01-02 - Run [0bNNs5GTru0].en.srt", "0bNNs5GTru0"},
		// A title's own brackets do not win over the ID that closes the name.
		{"2019-05-01 - [Compilation] Best Runs [0bNNs5GTru0].mkv", "0bNNs5GTru0"},
		{"2025-04-12 - Marble Survival S2 - E5 [Race 15 - 17] [_TZW4HHkF1s].mkv", "_TZW4HHkF1s"},
		// An ID ahead of the title is still found.
		{"[0bNNs5GTru0] Run.mkv", "0bNNs5GTru0"},
		// The folder's tag is not the file's.
		{"/media/Show [0bNNs5GTru0]/2024-01-02 - Run.mkv", ""},
		{"2024-01-02 - Run.mkv", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got, ok := VideoIDFromPath(tc.path)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("VideoIDFromPath(%q) = %q, %v; want %q", tc.path, got, ok, tc.want)
		}
	}
	for id, want := range map[string]bool{"0bNNs5GTru0": true, "_TZW4HHkF1s": true, "UCA94TZluH5eQtkQpaCZQfRw": false, "PLrpqCP1_qdKhShZPq": false, "": false, "0bNNs5GTru": false} {
		if IsVideoID(id) != want {
			t.Errorf("IsVideoID(%q) = %v", id, !want)
		}
	}
}

func TestRepairedRefs(t *testing.T) {
	// One underscore: one candidate of the right length per spelling.
	refs := RepairedRefs("Matthew Wood [UC8WBLj aquoUjJkXPXMC5aA]")
	if len(refs) != 1 || refs[0] != (Ref{RefChannel, "UC8WBLj_aquoUjJkXPXMC5aA"}) {
		t.Errorf("single underscore: %+v", refs)
	}
	// "__" collapsed to one space: only the doubled spelling is 24 long.
	refs = RepairedRefs("Show [youtube-UCab cdefghijklmnopqrst]")
	if len(refs) != 1 || refs[0].ID != "UCab__cdefghijklmnopqrst" {
		t.Errorf("double underscore: %+v", refs)
	}
	// Two gaps that could each be either: every 24-character spelling.
	refs = RepairedRefs("[UCabc defghij klmnopqrs]")
	want := map[string]bool{"UCabc__defghij_klmnopqrs": true, "UCabc_defghij__klmnopqrs": true}
	if len(refs) != 2 || !want[refs[0].ID] || !want[refs[1].ID] {
		t.Errorf("two gaps: %+v", refs)
	}
	// A playlist tag loses its underscore the same way.
	refs = RepairedRefs("Mix [PL22J3VaeABQD IZs7y60I3lUrrFTzkpat]")
	if len(refs) != 1 || refs[0] != (Ref{RefPlaylist, "PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat"}) {
		t.Errorf("playlist: %+v", refs)
	}
	// The "yt-" prefix is set aside like "youtube-".
	refs = RepairedRefs("Matthew Wood {yt-UC8WBLj aquoUjJkXPXMC5aA}")
	if len(refs) != 1 || refs[0] != (Ref{RefChannel, "UC8WBLj_aquoUjJkXPXMC5aA"}) {
		t.Errorf("yt- prefix: %+v", refs)
	}
	// Tags without spaces, and ordinary bracketed words, give nothing.
	for _, text := range []string{"BrainBreakLand [UCA94TZluH5eQtkQpaCZQfRw]", "Show (PE & Brain Breaks)", "Show [UC is short]", "Plain title"} {
		if refs := RepairedRefs(text); len(refs) != 0 {
			t.Errorf("RepairedRefs(%q) = %+v", text, refs)
		}
	}
}

func TestParseVideoID(t *testing.T) {
	cases := map[string]string{
		"2024-01-02 - Mario's Winter Run [0bNNs5GTru0].mkv":       "0bNNs5GTru0",
		"Golf With Friends _ HOLE IN ONE [81er8CP24h8].mkv":       "81er8CP24h8",
		"clip [youtube-mKgl1CiA9Qk].mp4":                          "mKgl1CiA9Qk",
		"https://www.youtube.com/watch?v=f-wWBGo6a2w&list=PL22J3": "f-wWBGo6a2w",
		"https://youtu.be/dQw4w9WgXcQ":                            "dQw4w9WgXcQ",
	}
	for text, want := range cases {
		if got, ok := ParseVideoID(text); !ok || got != want {
			t.Errorf("ParseVideoID(%q) = %q, %v; want %q", text, got, ok, want)
		}
	}
	if got, ok := ParseVideoID("Brain Break Land"); ok {
		t.Errorf("ParseVideoID found %q in a plain title", got)
	}
}

func TestCleanQuery(t *testing.T) {
	cases := map[string]string{
		"Brain Break Land [youtube-UCA94TZluH5eQtkQpaCZQfRw]": "Brain Break Land",
		"  Danny Go!   {tvdb-123} ":                           "Danny Go!",
		"Matthew Wood (PE & Brain Breaks)":                    "Matthew Wood (PE & Brain Breaks)",
	}
	for in, want := range cases {
		if got := CleanQuery(in); got != want {
			t.Errorf("CleanQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"PT5M14S":  5*time.Minute + 14*time.Second,
		"PT1H2M3S": time.Hour + 2*time.Minute + 3*time.Second,
		"PT45S":    45 * time.Second,
		"P1DT2H":   26 * time.Hour,
		"P0D":      0,
		"garbage":  0,
		"":         0,
	}
	for in, want := range cases {
		if got := ParseDuration(in); got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsChannelID(t *testing.T) {
	if !IsChannelID("UCA94TZluH5eQtkQpaCZQfRw") {
		t.Error("a channel ID was not recognised")
	}
	for _, id := range []string{"PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat", "UCshort", "UUA94TZluH5eQtkQpaCZQfRw", ""} {
		if IsChannelID(id) {
			t.Errorf("IsChannelID(%q) = true", id)
		}
	}
}

func TestThumbnailsBest(t *testing.T) {
	thumbs := Thumbnails{
		"default": {URL: "d"},
		"high":    {URL: "h"},
		"maxres":  {URL: ""},
	}
	if got := thumbs.Best().URL; got != "h" {
		t.Errorf("Best() = %q, want the largest non-empty size %q", got, "h")
	}
}
