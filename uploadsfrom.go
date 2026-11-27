package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/collisionc/silo-plugin-metadata-youtube/provider"
)

// The two settings that say how far back uploads are listed: "uploads_from",
// a year for every channel and playlist, and "uploads_from_by_id", a year
// for one of them, as "UCxxxx=2023" entries. A channel downloaded whole and a
// channel followed since last year sit in one library, so one year for all
// would leave the first channel's older files without an episode.

var (
	// A channel or playlist ID: what an entry may name.
	seriesIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{12,64}$`)
	// Entries are split at commas, semicolons and line ends.
	entrySplitRe = regexp.MustCompile(`[,;\r\n]+`)
	// An ID and a year, with "=", ":" or a space between them.
	entryRe = regexp.MustCompile(`^(\S+?)\s*(?:=|:|\s)\s*(\S+)$`)
)

// YouTube opened in 2005; the upper bound only catches a typing mistake.
const firstYouTubeYear, lastListYear = 2005, 2100

// listYear reads a year, or "" / "all" / "0" for every upload (0).
func listYear(text string) (int, bool) {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" || text == "all" || text == "0" {
		return 0, true
	}
	year, err := strconv.Atoi(text)
	if err != nil || year < firstYouTubeYear || year > lastListYear {
		return 0, false
	}
	return year, true
}

// uploadsFrom reads the two settings. What cannot be read is left out, so
// the uploads it was about are all listed, and is named in problems.
func (s settings) uploadsFrom() (from provider.UploadsFrom, problems []string) {
	year, ok := listYear(s.uploadsFromYear)
	if !ok {
		problems = append(problems, fmt.Sprintf("%q is not a year to list uploads from. Use a year such as 2023, or leave it empty for every upload.", s.uploadsFromYear))
	}
	from.Year = year
	for _, entry := range entrySplitRe.Split(s.uploadsFromByID, -1) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		m := entryRe.FindStringSubmatch(entry)
		if m == nil || !seriesIDRe.MatchString(m[1]) {
			problems = append(problems, fmt.Sprintf("%q is not a channel or playlist ID and a year, such as UCuk26DMu7ewAkC-ntx7esiA=2023.", entry))
			continue
		}
		year, ok := listYear(m[2])
		if !ok {
			problems = append(problems, fmt.Sprintf("%q: %q is not a year. Use a year such as 2023, or all for every upload.", entry, m[2]))
			continue
		}
		if from.ByID == nil {
			from.ByID = make(map[string]int)
		}
		from.ByID[m[1]] = year
	}
	return from, problems
}
