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
		// Pasted URLs and handles in a manual search.
		{"https://www.youtube.com/channel/UCYzPXprvl5Y-Sf0g4vX-m6g", Ref{RefChannel, "UCYzPXprvl5Y-Sf0g4vX-m6g"}, true},
		{"https://www.youtube.com/watch?v=f-wWBGo6a2w&list=PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat", Ref{RefPlaylist, "PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat"}, true},
		{"https://www.youtube.com/@dannygo", Ref{RefHandle, "dannygo"}, true},
		{"@brainbreakland", Ref{RefHandle, "brainbreakland"}, true},
		// Plain titles carry no ID: a word starting with PL or UC is not one.
		{"Brain Break Land", Ref{}, false},
		{"PLAYGROUND_ADVENTURES_FOREVER_AND_EVER", Ref{}, false},
		{"Matthew Wood (PE & Brain Breaks)", Ref{}, false},
		{"name@example.com", Ref{}, false},
	}
	for _, tc := range cases {
		got, ok := ParseRef(tc.text)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v, %v", tc.text, got, ok, tc.want, tc.ok)
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
