package youtube

import (
	"regexp"
	"strings"
	"time"
)

// RefKind says what a reference names.
type RefKind string

const (
	RefChannel  RefKind = "channel"
	RefPlaylist RefKind = "playlist"
	RefHandle   RefKind = "handle"
	RefVideo    RefKind = "video"
)

// Ref is a YouTube identifier found in a folder name, a search typed by an
// administrator, or a pasted URL.
type Ref struct {
	Kind RefKind
	ID   string
}

const idChars = `A-Za-z0-9_-`

var (
	// A channel ID is UC followed by 22 characters. ZeroQI's Absolute Series
	// Scanner writes it as [youtube-UC...], [youtube2-UC...], {youtube-UC...}
	// or a bare [UC...]; a URL has /channel/UC.... The bounds keep it from
	// matching inside a longer token.
	channelIDRe = regexp.MustCompile(`(?:^|[^` + idChars + `]|(?i:youtube[0-9]?)-)(UC[` + idChars + `]{22})(?:$|[^` + idChars + `])`)
	// Playlist IDs (PL..., and the UU/FL/LP/RD/OL families) are only taken
	// from a bracketed tag or a list= URL parameter: a bare word starting with
	// PL is too likely to be part of a title.
	playlistTagRe = regexp.MustCompile(`(?i)[\[\{\(](?:youtube[0-9]?-)?((?:PL|UU|FL|LP|RD|OL)[` + idChars + `]{10,48})[\]\}\)]`)
	playlistURLRe = regexp.MustCompile(`[?&]list=((?:PL|UU|FL|LP|RD|OL)[` + idChars + `]{10,48})`)
	// An @handle on its own, after a space, or in a youtube.com/@handle URL.
	handleRe = regexp.MustCompile(`(?:^|[\s/])@([A-Za-z0-9._-]{3,30})(?:$|[\s/?#])`)
	// A video ID in brackets, as yt-dlp's "%(title)s [%(id)s]" writes it, or
	// in a watch?v= / youtu.be URL.
	videoTagRe = regexp.MustCompile(`(?i)\[(?:youtube-)?([` + idChars + `]{11})\]`)
	videoURLRe = regexp.MustCompile(`(?:[?&]v=|youtu\.be/|/shorts/)([` + idChars + `]{11})(?:$|[^` + idChars + `])`)

	bracketTagRe = regexp.MustCompile(`\s*(?:\[[^\]]*\]|\{[^}]*\})`)
	spaceRe      = regexp.MustCompile(`\s+`)
)

// ParseRef finds the strongest YouTube reference in text: a playlist tag or
// URL, then a channel ID, then an @handle. Video IDs are left to
// ParseVideoID, since a series is never a single video.
func ParseRef(text string) (Ref, bool) {
	if m := playlistTagRe.FindStringSubmatch(text); m != nil {
		return Ref{Kind: RefPlaylist, ID: m[1]}, true
	}
	if m := playlistURLRe.FindStringSubmatch(text); m != nil {
		return Ref{Kind: RefPlaylist, ID: m[1]}, true
	}
	if m := channelIDRe.FindStringSubmatch(text); m != nil {
		return Ref{Kind: RefChannel, ID: m[1]}, true
	}
	if m := handleRe.FindStringSubmatch(text); m != nil {
		return Ref{Kind: RefHandle, ID: m[1]}, true
	}
	return Ref{}, false
}

// ParseVideoID finds a video ID in a file name or URL.
func ParseVideoID(text string) (string, bool) {
	if m := videoURLRe.FindStringSubmatch(text); m != nil {
		return m[1], true
	}
	if m := videoTagRe.FindStringSubmatch(text); m != nil {
		return m[1], true
	}
	return "", false
}

// IsChannelID reports whether id has the shape of a channel ID.
func IsChannelID(id string) bool {
	return len(id) == 24 && strings.HasPrefix(id, "UC") && channelIDRe.MatchString(id)
}

// CleanQuery strips bracketed tags and extra spaces from a folder title, for a
// free-text channel search.
func CleanQuery(text string) string {
	text = bracketTagRe.ReplaceAllString(text, " ")
	return strings.TrimSpace(spaceRe.ReplaceAllString(text, " "))
}

// ParseTime reads an RFC 3339 timestamp; the zero time when empty or invalid.
func ParseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

// isoDurationRe reads the ISO 8601 durations the API returns (PT1H2M3S,
// P1DT2H, PT45S).
var isoDurationRe = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// ParseDuration reads an ISO 8601 duration; zero when it cannot.
func ParseDuration(value string) time.Duration {
	m := isoDurationRe.FindStringSubmatch(value)
	if m == nil {
		return 0
	}
	var total time.Duration
	units := []time.Duration{24 * time.Hour, time.Hour, time.Minute, time.Second}
	for i, unit := range units {
		if m[i+1] == "" {
			continue
		}
		n := 0
		for _, r := range m[i+1] {
			n = n*10 + int(r-'0')
		}
		total += time.Duration(n) * unit
	}
	return total
}
