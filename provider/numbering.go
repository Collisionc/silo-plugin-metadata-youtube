package provider

import (
	"sort"
	"time"
)

// Upload is one video of a channel or playlist, as its listing reports it.
type Upload struct {
	VideoID   string
	Title     string
	Published time.Time
}

// NumberedUpload is an upload placed in a season and given an episode number.
type NumberedUpload struct {
	Upload
	Season  int
	Episode int
	// AirDate is the upload's date (YYYY-MM-DD) in the configured time zone.
	// Silo links a dated file ("2024-01-02 - Title [id].mkv") to the episode
	// with that air date, so it must be the same date the downloader put in
	// the file name.
	AirDate string
}

// maxPerDay is the most uploads one day can number without spilling into the
// next day's numbers.
const maxPerDay = 99

// Number places uploads the way ZeroQI's Absolute Series Scanner places a
// channel: the year is the season, and the episode number is MMDDxx, xx
// counting that day's uploads from 01 in upload order. January 2nd's first
// video is S2024E10201, its second S2024E10202.
//
// The numbers stay put as a channel grows: a new upload only takes the next
// number of its own day. Removing an earlier video from the same day shifts
// the later ones of that day, which is rare.
//
// Uploads without a publish time cannot be placed and are left out. The
// result is sorted by season and episode.
func Number(uploads []Upload, loc *time.Location) []NumberedUpload {
	if loc == nil {
		loc = time.UTC
	}
	sorted := make([]Upload, 0, len(uploads))
	seen := make(map[string]struct{}, len(uploads))
	for _, upload := range uploads {
		if upload.Published.IsZero() || upload.VideoID == "" {
			continue
		}
		if _, dup := seen[upload.VideoID]; dup {
			continue
		}
		seen[upload.VideoID] = struct{}{}
		sorted = append(sorted, upload)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].Published.Equal(sorted[j].Published) {
			return sorted[i].Published.Before(sorted[j].Published)
		}
		return sorted[i].VideoID < sorted[j].VideoID
	})

	out := make([]NumberedUpload, 0, len(sorted))
	perDay := make(map[string]int)
	for _, upload := range sorted {
		local := upload.Published.In(loc)
		airDate := local.Format(time.DateOnly)
		perDay[airDate]++
		n := min(perDay[airDate], maxPerDay)
		out = append(out, NumberedUpload{
			Upload:  upload,
			Season:  local.Year(),
			Episode: int(local.Month())*10000 + local.Day()*100 + n,
			AirDate: airDate,
		})
	}
	return out
}
