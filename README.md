# clack

A GitHub Actions **departures board**: a live, cross-repo view of workflow
runs styled like an airport split-flap display. `gh run watch` follows one
run; clack shows everything at once.

```sh
go install github.com/jzbz/clack@latest
```

```
CLACK · GITHUB ACTIONS DEPARTURES                             20:17:14
TIME  REPO          WORKFLOW     BRANCH    ACTOR    STATUS     AGE
20:13 ACME/WEB      CI           MAIN      OCTOCAT  BOARDING   4M
20:16 ACME/MOBILE   TESTS        FEAT/TABS MONA     QUEUED     38S
20:03 ACME/INFRA    TERRAFORM    MAIN      OCTOCAT  DEPARTED   7M04S
19:51 ACME/WEB      LINT         FIX/CSS   MONA     CANCELLED  1M38S
```

Status vocabulary: in progress → `BOARDING` (blinks), success → `DEPARTED`,
failure/cancelled/timed out → `CANCELLED`, queued → `QUEUED`, waiting →
`WAITING`, skipped → `SKIPPED`, action required → `HOLD`, finished with no
conclusion → `ENDED`. `AGE` is how long a live run has been going, or
exactly how long a finished one took. A live age is rounded to the minute
once past one: a flap cell takes about a second to turn, so a running
seconds counter would never settle.

The board sizes itself to the terminal: the workflow column takes up the
slack, and on a narrow one the actor, branch and time columns drop off
rather than running past the edge.

## Usage

Auth comes from `$GITHUB_TOKEN`, falling back to `gh auth token`.

```sh
clack --repo owner/name --repo owner/other          # repeatable, comma-separated
clack --demo                                        # simulated board, no API
```

| Flag         | Default             | Meaning                          |
| ------------ | ------------------- | -------------------------------- |
| `--repo`     | —                   | repo to watch (`OWNER/NAME`)     |
| `--interval` | `30s` (min `10s`)   | poll interval                    |
| `--rows`     | `12`                | max rows on the board            |
| `--per-repo` | `5`                 | runs fetched per repo per poll   |
| `--config`   | `~/.config/clack/config.yaml` | config file path       |

Config file (all keys optional; flags win):

```yaml
repos:
  - owner/name
  - owner/other
interval: 20s
rows: 15
per_repo: 5
```

Keys: `↑`/`k` `↓`/`j` select · `g`/`G` (or `home`/`end`) top/bottom ·
`o`/`enter` open run in browser · `r` poll now · `?` help · `q` quit.

Unknown keys in the config file are an error rather than a setting that
silently never applies, and a `--config` path that does not exist is only
ignored when it is the default one.

## Rate limits

One API request per repo per poll, and most of them are free: each repo's
`ETag` is kept and sent back as `If-None-Match`, so a repo with nothing new
answers `304 Not Modified`, which GitHub does not charge against the rate
limit. The board reads `X-RateLimit-*` headers, shows the tightest remaining
budget in the footer, waits out an exhausted limit until its reset time, and
backs off exponentially (to 5 min) on consecutive errors.

## Idle cost

The split-flap animation runs at 20 fps, but only while something is
actually moving. Once every row has settled the board schedules its next
repaint for the moment the display can next change — the next blink of a
`BOARDING` row, or the next tick of the clock — so a quiet board redraws
about once a second instead of twenty times.

## Develop

```sh
go test ./...                             # unit tests
go test ./internal/board/ -bench .        # frame and merge costs
CLACK_LIVE=1 go test ./internal/ghapi/    # three real API calls
CLACK_SNAPSHOT=/tmp/frames go test ./internal/board/ -run TestSnapshot
```

The live tests check that a run comes back whole, and that a second poll of
an unchanged repo is revalidated with its ETag and costs no quota.

`TestSnapshot` writes rendered frames — mid-flap, settled, narrow, and with
the help and error lines up — as plain text, so the board can be inspected
without a terminal.
