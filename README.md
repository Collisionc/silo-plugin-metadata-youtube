# YouTube Metadata for Silo

A [Silo](https://github.com/Silo-Server/silo-server) metadata provider plugin
that shows downloaded YouTube channels and playlists as TV series, in the way
the [Sportarr plugin](https://github.com/Silo-Server/silo-plugin-metadata-sportarr)
shows sports leagues:

| YouTube | Silo |
|---|---|
| Channel (or playlist) | Series |
| Year with uploads | Season (`2024`) |
| Video | Episode `MMDDxx`, dated by its upload |

Titles, descriptions, durations and thumbnails come from the
[YouTube Data API v3](https://developers.google.com/youtube/v3). The series
poster is the channel's avatar; a backdrop (the channel banner or newest
thumbnail) is optional.

## How files are matched

The numbering and folder tags follow ZeroQI's
[Absolute Series Scanner](https://github.com/ZeroQI/Absolute-Series-Scanner)
and [YouTube-Agent](https://github.com/ZeroQI/YouTube-Agent.bundle) for Plex,
so a library laid out for them works here too.

**The series.** Silo searches the provider with the series title it reads from
the folder name. The plugin looks for, in order:

1. a playlist ID in a tag: `[youtube-PLxxxx]`, `[PLxxxx]`, or a `list=` URL;
2. a channel ID: `[youtube-UCxxxx]`, `[youtube2-UCxxxx]`, `{youtube-UCxxxx}`,
   `[UCxxxx]`, or a `/channel/UCxxxx` URL, so
   `BrainBreakLand [UCA94TZluH5eQtkQpaCZQfRw]` and
   `Brain Break Land [youtube-UCA94TZluH5eQtkQpaCZQfRw]` both work;
3. an `@handle`, alone or in a `youtube.com/@handle` URL;
4. otherwise a YouTube channel search for the folder name without its
   bracketed tags (100 quota units), offering the top five channels.

Silo turns underscores in a folder name into spaces before it searches, so a
channel ID such as `UC8WBLj_aquoUjJkXPXMC5aA` arrives as
`UC8WBLj aquoUjJkXPXMC5aA`. The plugin rebuilds the IDs a bracketed tag can
have held and looks them up in one call (1 quota unit), so an ID with
underscores in a tag still matches.

Silo checks a provider's answer against the folder title before accepting it,
and a lone answer needs more than a YouTube channel can score (no TMDB, TVDB
or IMDb ID, no second provider, no year in the folder name). So when the
folder names a channel outright, the plugin returns the folder title as an
alias of the channel, and the channel's uploads playlist ("Uploads from
...") as a second candidate; the channel then wins clearly. If a channel
stays unmatched, use **Match** on it and pick the channel (the uploads
playlist lists the same videos and works too).

A playlist folder such as `BattleBots [PLrpqCP1_qdKhShZPqyWk9xJp0HIqRKc-t]`
works the same way: the playlist comes back with the folder title as an alias,
and its owner's uploads playlist as the weaker second candidate.

A folder named exactly after the channel (`Brain Break Land`) usually matches
by search. Put the channel ID in the name when a search picks the wrong
channel, or use Silo's **Match** with the channel URL or `@handle`.

Unlike Absolute Series Scanner, Silo does not read a `youtube.id` file: it
passes plugins the folder name only.

**The episodes.** Each year with uploads is a season. A video's episode number
is `MMDDxx`: month, day, and its place among that day's uploads, from `01`.
January 2nd's first upload is `S2024E10201`, its second `S2024E10202`. New
uploads never renumber older episodes. The numbers only put the videos in
order, so the plugin asks Silo's clients to label each episode with its
upload date instead.

With **Seasons** set to **One season**, every video is in season 1 and the
year leads the number instead: `S1E2024010201`. Changing the setting
renumbers every episode, so choose it before a channel has watch history,
then refresh the channels' metadata.

Silo links a file to an episode by the **date in its name**, so name files with
their upload date, for example TubeSync's
`{yyyy_mm_dd} - {title_full} [{key}].{ext}` or yt-dlp's
`%(upload_date>%Y-%m-%d)s - %(title)s [%(id)s].%(ext)s`:

```
YouTube/
  Brain Break Land/
    2024-01-02 - Mario's Winter Run [0bNNs5GTru0].mkv
    2024-01-02 - Second Run Of The Day [xxxxxxxxxxx].mkv
  Some Playlist [youtube-PL22J3VaeABQD_IZs7y60I3lUrrFTzkpat]/
    2023-05-06 - First Video [yyyyyyyyyyy].mkv
```

Two uploads on the same day are told apart by the title in the file name.
Season folders are optional.

Dates are compared in UTC, as yt-dlp and TubeSync write them. If your
downloader dates files in local time, set **Upload date time zone** to match;
otherwise a video uploaded near midnight links to no episode.

## Setup

1. Create a YouTube Data API v3 key: Google Cloud console, a project, **APIs &
   Services > Library > YouTube Data API v3 > Enable**, then **Credentials >
   Create credentials > API key**. Restricting it to the YouTube Data API is
   a good idea.
2. Install the plugin: download the binary for your server's platform from the
   [releases](../../releases) (`plugin-linux-amd64` for most servers) and
   upload it under **Admin > Plugins**. Each release binary carries a signed
   build provenance attestation; to check that a download was built by this
   repository's release workflow:
   `gh attestation verify plugin-linux-amd64 --repo Collisionc/silo-plugin-metadata-youtube`.
   To build it yourself, see [Building from source](#building-from-source).
3. Under the plugin's settings, enter the API key. **Seasons** chooses a
   season per year or one season; **Backdrop** chooses the series background
   art: none (the default, as banners and thumbnails carry lettering that
   makes the description hard to read), the channel banner, or the newest
   video's thumbnail. **Check Connection** tries the key with one call
   (1 quota unit) and says what to fix if YouTube refuses it.
4. Keep the channels in a TV library of their own, and in that library
   enable **YouTube** as the only series metadata provider: turn TVDB and TMDB
   off there. The plugin installs disabled, so other libraries are not
   affected. Putting YouTube first in the chain is not enough: Silo's
   automatic matching sets aside every candidate without a TVDB, TMDB or IMDb
   ID as soon as any provider returns one, so a TV show that shares a
   channel's name would win, or leave the channel unmatched.
5. Scan the library.

## Quota

A key has 10,000 units a day by default. A channel search costs 100 units;
every other call costs 1 unit per 50 results. Refreshing a channel of 1,000
videos costs about 40 units: 20 to list it, 20 for the videos' details. The
plugin caches listings for an hour and video details for a day.

A channel search happens only for a folder whose name holds no channel or
playlist ID or @handle, and Silo repeats it on every scan of that folder
until it is matched. The plugin keeps each search's answer for an hour, an
empty one too, so a folder scanned all day costs at most 2,400 units, not
100 per scan. Putting the ID in the folder name (`[youtube-UC...]`) avoids
searches altogether.

When the quota runs out, YouTube answers `quotaExceeded` until midnight
Pacific time, and metadata calls fail until then. The plugin tells Silo the
failure is temporary, as it does when YouTube cannot be reached, so Silo
keeps retrying an unmatched folder (waiting longer each time, up to a day)
rather than giving up on it. A missing or rejected key is reported as a
settings problem; changing the settings makes Silo try again.

Silo waits 30 seconds for each answer, and listing a channel of several
thousand videos takes longer. The listing then carries on in the background
and Silo's next try finds it ready, without listing the channel again. An
episode list that runs out of time comes back with each video's title and
upload date, which is enough to link the files; descriptions, durations and
thumbnails fetched meanwhile arrive with the next refresh.

## What it logs

The plugin writes its log lines as hclog JSON on stderr. Silo's plugin host
passes such a line on to Silo's log at the level it names; a line in any
other form is shown at debug level only, which Silo's log does not keep.

- `youtube: started`, with the version, each time Silo starts the plugin
  (Silo stops an idle plugin after 30 minutes).
- `youtube: a lookup as Silo sent it`, or as a warning
  `youtube: a lookup that found nothing, as Silo sent it`: the first two
  lookups of each shape - which call, and which IDs and file path it carried -
  while the plugin runs, with their values. Silo itself says only
  `no metadata found from any provider` when a refresh fails; this shows what
  the refresh sent.
- `youtube: what Silo asked in 5 minutes`: the lookups of a busy spell,
  counted by call and outcome.

## Building from source

You need only [Go](https://go.dev/dl/), version 1.26 or newer. You can build
on Windows, macOS or Linux, whatever your Silo server runs on: Go builds for
another platform as readily as for its own.

1. **Install Go.** Download the installer for the computer you are building
   on from [go.dev/dl](https://go.dev/dl/) and run it. Then open a new
   terminal (PowerShell on Windows) and check it:

   ```sh
   go version
   ```

   It should print `go version go1.26` or later.

2. **Get the source.** On the
   [repository's GitHub page](https://github.com/Collisionc/silo-plugin-metadata-youtube)
   choose **Code > Download ZIP**, extract it, and open a terminal in the
   extracted folder. Or, if you have [Git](https://git-scm.com/downloads):

   ```sh
   git clone https://github.com/Collisionc/silo-plugin-metadata-youtube.git
   cd silo-plugin-metadata-youtube
   ```

3. **Pick your server's platform.** This is where Silo runs, not where you
   build:

   | Silo server | `GOOS` | `GOARCH` |
   | --- | --- | --- |
   | Linux on Intel or AMD (most servers) | `linux` | `amd64` |
   | Linux on 64-bit ARM, such as a Raspberry Pi 4 or 5 with a 64-bit OS | `linux` | `arm64` |
   | macOS on Apple silicon | `darwin` | `arm64` |

   Silo in Docker counts as Linux, on the host's processor.

4. **Build.** These commands build for `linux` and `amd64`. For another
   platform, change the two values and the file name to match, for example
   `dist/plugin-linux-arm64`.

   macOS or Linux:

   ```sh
   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/plugin-linux-amd64 .
   ```

   Windows (PowerShell):

   ```powershell
   $env:GOOS = 'linux'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
   go build -trimpath -ldflags="-s -w" -o dist/plugin-linux-amd64 .
   ```

   The first build downloads the plugin's dependencies, so it needs an
   internet connection. On Windows, close the PowerShell window afterwards:
   the three settings last as long as it stays open, and would apply to any
   later `go` command in it.

5. **Install it.** The plugin is the single file `dist/plugin-linux-amd64`.
   Upload it under **Admin > Plugins** and carry on from step 3 of
   [Setup](#setup).

With `make` and a POSIX shell, `make build-all` builds all three platforms at
once.

## Development

```sh
go test ./...
make build-all
# Against the real API, about 110 units:
YOUTUBE_API_KEY=... go test -run TestLive -v ./provider/
```

The SDK, `github.com/Silo-Server/silo-plugin-sdk`, is pinned to the version
the targeted Silo build uses (v0.35.0). Use a local `go.work` for SDK
development; never commit a filesystem `replace`.

Pushing a `v*` tag builds the binaries and publishes a GitHub release.

## Notes

- Shorts and past live streams are uploads like any other and become episodes.
  Silo learns about every upload, including the ones you have not
  downloaded; those are episodes without a file.
- Private and deleted videos are left out; a video deleted after its listing
  keeps the title it was listed with.
- Please respect the [YouTube Terms of Service](https://www.youtube.com/t/terms).

## License

AGPL-3.0-or-later, like Silo's own plugins. See [LICENSE](LICENSE).
