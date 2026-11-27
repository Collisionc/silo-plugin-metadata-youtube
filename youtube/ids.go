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

// tagPrefix is what may stand before an ID in a tag: "youtube-" (ZeroQI's
// scanner also writes "youtube2-") or the shorter "yt-", in any case.
const tagPrefix = `(?i:youtube[0-9]?|yt)-`

var (
	// A channel ID is UC followed by 22 characters. ZeroQI's Absolute Series
	// Scanner writes it as [youtube-UC...], [youtube2-UC...], {youtube-UC...}
	// or a bare [UC...], and {yt-UC...} is read too; a URL has
	// /channel/UC.... The bounds keep it from matching inside a longer token.
	channelIDRe = regexp.MustCompile(`(?:^|[^` + idChars + `]|` + tagPrefix + `)(UC[` + idChars + `]{22})(?:$|[^` + idChars + `])`)
	// Playlist IDs (PL..., and the UU/FL/LP/RD/OL families) are only taken
	// from a bracketed tag or a list= URL parameter: a bare word starting with
	// PL is too likely to be part of a title.
	playlistTagRe = regexp.MustCompile(`(?i)[\[\{\(](?:` + tagPrefix + `)?((?:PL|UU|FL|LP|RD|OL)[` + idChars + `]{10,48})[\]\}\)]`)
	playlistURLRe = regexp.MustCompile(`[?&]list=((?:PL|UU|FL|LP|RD|OL)[` + idChars + `]{10,48})`)
	// An @handle on its own, after a space, or in a youtube.com/@handle URL.
	handleRe = regexp.MustCompile(`(?:^|[\s/])@([A-Za-z0-9._-]{3,30})(?:$|[\s/?#])`)
	// A video ID in square brackets, as yt-dlp's "%(title)s [%(id)s]" writes
	// it, or in curly ones, with or without a prefix: [0bNNs5GTru0],
	// {0bNNs5GTru0}, [youtube-0bNNs5GTru0], {yt-0bNNs5GTru0}.
	videoTagRe = regexp.MustCompile(`(?i)[\[\{](?:` + tagPrefix + `)?([` + idChars + `]{11})[\]\}]`)
	// The same tag closing a file's name, before its extensions.
	videoTagAtEndRe = regexp.MustCompile(`(?i)[\[\{](?:` + tagPrefix + `)?([` + idChars + `]{11})[\]\}](?:\.[a-z0-9]{1,8})*$`)
	videoIDRe       = regexp.MustCompile(`^[` + idChars + `]{11}$`)
	videoURLRe      = regexp.MustCompile(`(?:[?&]v=|youtu\.be/|/shorts/|/live/|/embed/)([` + idChars + `]{11})(?:$|[^` + idChars + `])`)
	// A tag that may name a video, as Silo hands a movie's title over: the ID
	// alone in square or curly brackets, or after "youtube-" or "yt-" in any
	// brackets. Silo turns "_" into a space, so the ID can arrive with
	// spaces in it.
	videoNameTagRe = regexp.MustCompile(`(\[|\{|\()(` + tagPrefix + `)?([ ` + idChars + `]{1,11})[\]\}\)]`)

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

var (
	// spacedTagRe is a bracketed tag whose content may have lost its
	// underscores: Silo reads a folder title with "_" turned into spaces and
	// runs of spaces collapsed, so [UC8WBLj_aquoUjJkXPXMC5aA] reaches the
	// plugin as [UC8WBLj aquoUjJkXPXMC5aA], and a "__" as one space.
	spacedTagRe   = regexp.MustCompile(`[\[\{\(]((?:` + tagPrefix + `)?(?:UC|PL|UU|FL|LP|RD|OL)[ ` + idChars + `]{8,60})[\]\}\)]`)
	tagPrefixRe   = regexp.MustCompile(`^` + tagPrefix)
	idPartRe      = regexp.MustCompile(`^[` + idChars + `]+$`)
	maxRepairGaps = 6 // 2^6 = 64 candidates, more than one lookup's 50 IDs
)

// RepairedRefs rebuilds the IDs a bracketed tag can have held before its
// underscores became spaces. Each space stands for "_" or "__"; only
// candidates of a valid length are returned, at most 50 per kind, so one
// lookup can tell which exists. It returns nothing for a tag without spaces,
// which ParseRef already reads.
func RepairedRefs(text string) []Ref {
	var refs []Ref
	seen := make(map[string]bool)
	counts := make(map[RefKind]int)
	for _, m := range spacedTagRe.FindAllStringSubmatch(text, -1) {
		content := tagPrefixRe.ReplaceAllString(m[1], "")
		parts := strings.Fields(content)
		if len(parts) < 2 || len(parts)-1 > maxRepairGaps {
			continue
		}
		valid := true
		for _, part := range parts {
			if !idPartRe.MatchString(part) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		gaps := len(parts) - 1
		for mask := 0; mask < 1<<gaps; mask++ {
			var b strings.Builder
			b.WriteString(parts[0])
			for i := 1; i < len(parts); i++ {
				if mask&(1<<(i-1)) != 0 {
					b.WriteString("__")
				} else {
					b.WriteString("_")
				}
				b.WriteString(parts[i])
			}
			id := b.String()
			kind, ok := idKind(id)
			if !ok || seen[id] || counts[kind] >= 50 {
				continue
			}
			seen[id] = true
			counts[kind]++
			refs = append(refs, Ref{Kind: kind, ID: id})
		}
	}
	return refs
}

// idKind says whether id has a channel or playlist ID's length.
func idKind(id string) (RefKind, bool) {
	switch {
	case strings.HasPrefix(id, "UC"):
		return RefChannel, len(id) == 24
	case strings.HasPrefix(id, "PL"):
		return RefPlaylist, len(id) == 18 || len(id) == 34
	case strings.HasPrefix(id, "UU"), strings.HasPrefix(id, "FL"), strings.HasPrefix(id, "LP"):
		return RefPlaylist, len(id) == 24
	case strings.HasPrefix(id, "RD"), strings.HasPrefix(id, "OL"):
		return RefPlaylist, len(id) >= 12 && len(id) <= 50
	}
	return "", false
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

// VideoIDFromURL finds the video a watch, youtu.be, shorts, live or embed URL
// names.
func VideoIDFromURL(text string) (string, bool) {
	if m := videoURLRe.FindStringSubmatch(text); m != nil {
		return m[1], true
	}
	return "", false
}

const (
	videoIDLength = 11
	// maxVideoCandidates is how many IDs one videos.list call looks up.
	maxVideoCandidates = 50
)

// VideoIDCandidates lists the video IDs a movie's title, or a search typed by
// an administrator, may name, the likeliest first: the ID in a video URL,
// then the tags from the last to the first. A tag is the ID alone in square
// or curly brackets ("Title [0bNNs5GTru0]", as yt-dlp names a file), or
// after "youtube-" or "yt-" in any brackets ("{youtube-0bNNs5GTru0}").
//
// Silo reads a title with every run of "_" turned into one space, so
// [a_b-c_d__eF] arrives as [a b-c d eF]. For such a tag the candidates are
// the 11-character IDs it can have been, each space standing for "_" or
// "__". A title's own bracketed words can be candidates too
// ("[Compilation]"), so the caller asks YouTube which exist: at most 50 are
// returned, one call's worth.
func VideoIDCandidates(text string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(id string) {
		if !seen[id] && len(out) < maxVideoCandidates {
			seen[id] = true
			out = append(out, id)
		}
	}
	if id, ok := VideoIDFromURL(text); ok {
		add(id)
	}
	tags := videoNameTagRe.FindAllStringSubmatch(text, -1)
	for i := len(tags) - 1; i >= 0; i-- {
		open, prefixed, content := tags[i][1], tags[i][2] != "", tags[i][3]
		if !prefixed && open == "(" {
			continue // "(Compilation)" is a title's own words
		}
		for _, id := range underscoredIDs(content) {
			add(id)
		}
		// An ID can itself begin "yt-": then the whole tag is the ID.
		if prefixed && open != "(" {
			for _, id := range underscoredIDs(tags[i][2] + content) {
				add(id)
			}
		}
	}
	return out
}

// underscoredIDs returns the 11-character IDs content can have been before
// its underscores became spaces: content itself when it has no space, and
// otherwise every way of putting "_" or "__" in place of each space that
// makes 11 characters. (A longer run of underscores is not rebuilt: an ID
// hardly ever has one, and a title's bracketed words would become
// candidates far more often.)
func underscoredIDs(content string) []string {
	parts := strings.Split(content, " ")
	gaps := len(parts) - 1
	missing := videoIDLength - (len(content) - gaps)
	if gaps == 0 {
		if missing == 0 {
			return []string{content}
		}
		return nil
	}
	if missing < gaps || missing > 2*gaps {
		return nil
	}
	var out []string
	var fill func(gap, left int, built string)
	fill = func(gap, left int, built string) {
		if len(out) >= maxVideoCandidates {
			return
		}
		if gap == gaps {
			out = append(out, built)
			return
		}
		after := gaps - gap - 1 // the gaps still to fill, with one or two each
		for n := 1; n <= 2; n++ {
			if rest := left - n; rest >= after && rest <= 2*after {
				fill(gap+1, rest, built+strings.Repeat("_", n)+parts[gap+1])
			}
		}
	}
	fill(0, missing, parts[0])
	return out
}

// IsVideoID reports whether id has the shape of a video ID: 11 characters.
// A channel or playlist ID never does; the shortest is 12.
func IsVideoID(id string) bool {
	return videoIDRe.MatchString(id)
}

// VideoIDFromPath finds the video a downloaded file holds, from the ID that
// yt-dlp and TubeSync put in brackets at the end of its name
// ("2024-01-02 - Title [0bNNs5GTru0].mkv"), in square or curly brackets and
// with or without "youtube-" or "yt-" before it. A tag closing the name is
// taken first, then the last one in it: a title can hold an eleven-letter
// word in brackets of its own ("[Compilation] Best Runs [0bNNs5GTru0]"), and
// a folder can end in a tag too.
func VideoIDFromPath(path string) (string, bool) {
	name := path
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if m := videoTagAtEndRe.FindStringSubmatch(name); m != nil {
		return m[1], true
	}
	if all := videoTagRe.FindAllStringSubmatch(name, -1); len(all) > 0 {
		return all[len(all)-1][1], true
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
