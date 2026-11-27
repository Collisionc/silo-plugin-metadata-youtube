package youtube

import (
	"slices"
	"strings"
	"testing"
)

func TestVideoIDFromURL(t *testing.T) {
	for text, want := range map[string]string{
		"https://www.youtube.com/watch?v=f-wWBGo6a2w&list=PL22J3": "f-wWBGo6a2w",
		"https://youtu.be/dQw4w9WgXcQ?t=42":                       "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/0bNNs5GTru0":              "0bNNs5GTru0",
		"https://www.youtube.com/live/0bNNs5GTru0?feature=share":  "0bNNs5GTru0",
		"https://www.youtube.com/embed/0bNNs5GTru0":               "0bNNs5GTru0",
	} {
		if got, ok := VideoIDFromURL(text); !ok || got != want {
			t.Errorf("VideoIDFromURL(%q) = %q, %v; want %q", text, got, ok, want)
		}
	}
	for _, text := range []string{"Mario's Winter Run [0bNNs5GTru0]", "https://www.youtube.com/@brainbreakland", "https://www.youtube.com/watch?v=tooShort"} {
		if got, ok := VideoIDFromURL(text); ok {
			t.Errorf("VideoIDFromURL(%q) found %q", text, got)
		}
	}
}

// The layouts of a channel folder and its files that must always work: the
// folder's name, which Silo hands over as the series title, names the
// channel, and the file's name names the video.
func TestChannelFolderAndFileTags(t *testing.T) {
	const channel, video = "UC2bkHVIDjXS7sgrgjFtzOXQ", "jiejNAUwcQ8"
	for _, path := range []string{
		"/mnt/pool/YouTube/video/engineerguyvideo {youtube-UC2bkHVIDjXS7sgrgjFtzOXQ}/2011-03-17 - LCD Monitor Teardown {youtube-jiejNAUwcQ8}.mkv",
		"/mnt/pool/YouTube/video/engineerguyvideo {yt-UC2bkHVIDjXS7sgrgjFtzOXQ}/2011-03-17 - LCD Monitor Teardown {yt-jiejNAUwcQ8}.mkv",
		"/mnt/pool/YouTube/video/engineerguyvideo {UC2bkHVIDjXS7sgrgjFtzOXQ}/2011-03-17 - LCD Monitor Teardown {jiejNAUwcQ8}.mkv",
		"/mnt/pool/YouTube/video/engineerguyvideo {UC2bkHVIDjXS7sgrgjFtzOXQ}/2011-03-17 - LCD Monitor Teardown {youtube-jiejNAUwcQ8}.mkv",
		"/mnt/pool/YouTube/video/engineerguyvideo [UC2bkHVIDjXS7sgrgjFtzOXQ]/2011-03-17 - LCD Monitor Teardown [jiejNAUwcQ8].mkv",
		"/mnt/pool/YouTube/video/engineerguyvideo [youtube-UC2bkHVIDjXS7sgrgjFtzOXQ]/2011-03-17 - LCD Monitor Teardown [youtube-jiejNAUwcQ8].mkv",
		"/mnt/pool/YouTube/video/engineerguyvideo [yt-UC2bkHVIDjXS7sgrgjFtzOXQ]/2011-03-17 - LCD Monitor Teardown [yt-jiejNAUwcQ8].mkv",
		"/mnt/pool/YouTube/video/engineerguyvideo {YouTube-UC2bkHVIDjXS7sgrgjFtzOXQ}/2011-03-17 - LCD Monitor Teardown {YT-jiejNAUwcQ8}.en.srt",
	} {
		parts := strings.Split(path, "/")
		folder := parts[len(parts)-2]
		if ref, ok := ParseRef(folder); !ok || ref != (Ref{RefChannel, channel}) {
			t.Errorf("ParseRef(%q) = %+v, %v; want the channel", folder, ref, ok)
		}
		if id, ok := VideoIDFromPath(path); !ok || id != video {
			t.Errorf("VideoIDFromPath(%q) = %q, %v; want %q", path, id, ok, video)
		}
		// The folder's tag is never taken for the file's.
		if id, ok := VideoIDFromPath(strings.Join(parts[:len(parts)-1], "/") + "/2011-03-17 - LCD Monitor Teardown.mkv"); ok {
			t.Errorf("VideoIDFromPath found %q in a file name without a tag", id)
		}
	}
}

// Titles as Silo hands them over for a movie: the file's name without its
// year and extension, with "_" turned into a space.
func TestVideoIDCandidates(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"Mario's Winter Run [0bNNs5GTru0]", []string{"0bNNs5GTru0"}},
		{"[0bNNs5GTru0]", []string{"0bNNs5GTru0"}},
		{"Mario's Winter Run {youtube-0bNNs5GTru0}", []string{"0bNNs5GTru0"}},
		{"Mario's Winter Run (YouTube-0bNNs5GTru0)", []string{"0bNNs5GTru0"}},
		{"Mario's Winter Run [yt-0bNNs5GTru0]", []string{"0bNNs5GTru0"}},
		{"Mario's Winter Run {0bNNs5GTru0}", []string{"0bNNs5GTru0"}},
		{"Some Talk {a b-c d-e-F}", []string{"a_b-c_d-e-F"}},
		// The last tag is the likeliest; a title's own bracketed word is asked
		// about as well.
		{"[Compilation] Best Runs [0bNNs5GTru0]", []string{"0bNNs5GTru0", "Compilation"}},
		{"https://youtu.be/dQw4w9WgXcQ", []string{"dQw4w9WgXcQ"}},
		// [a_b-c_d__eF], [-_b-c_d__eF] and [_bcdefghi__] as Silo passes them.
		{"Some Talk [a b-c d eF]", []string{"a_b-c_d__eF", "a_b-c__d_eF", "a__b-c_d_eF"}},
		{"Some Talk [- b-c d eF]", []string{"-_b-c_d__eF", "-_b-c__d_eF", "-__b-c_d_eF"}},
		{"Some Talk [ bcdefghi ]", []string{"_bcdefghi__", "__bcdefghi_"}},
		{"Some Talk {youtube-a b-c d-e-F}", []string{"a_b-c_d-e-F"}},
		// An ID can begin "yt-".
		{"Some Talk [yt-abcdefgh]", []string{"yt-abcdefgh"}},
		// Not IDs: the wrong length, bare in other brackets, a channel's.
		{"Mario's Winter Run", nil},
		{"Mario's Winter Run [2024]", nil},
		{"Part [2 of 3]", nil},
		{"Mario's Winter Run (0bNNs5GTru0)", nil},
		{"Mario's Winter Run (yt-0bNNs5GTru0)", []string{"0bNNs5GTru0"}},
		{"Brain Break Land [youtube-UCA94TZluH5eQtkQpaCZQfRw]", nil},
		{"Some Talk [My Top Ten Runs Ever]", nil},
	}
	for _, c := range cases {
		if got := VideoIDCandidates(c.text); !slices.Equal(got, c.want) {
			t.Errorf("VideoIDCandidates(%q) = %q, want %q", c.text, got, c.want)
		}
	}
	// One call's worth at most, however many tags a title has.
	many := ""
	for i := range 60 {
		many += "[" + string(rune('A'+i%26)) + string(rune('a'+i/26)) + "bcdefghij] "
	}
	if got := VideoIDCandidates(many); len(got) != 50 {
		t.Errorf("%d candidates, want 50", len(got))
	}
}
