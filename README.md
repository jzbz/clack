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
19:51 ACME/WEB      LINT         FIX/CSS   MONA     FAILED     1M38S
```

Status vocabulary: in progress → `BOARDING` (blinks), queued → `QUEUED`,
waiting → `WAITING`, success → `DEPARTED`, failure → `FAILED`, cancelled →
`CANCELLED`, timed out → `DELAYED`, skipped or stale → `SKIPPED`, action
required → `HOLD`, neutral or no conclusion → `ENDED`. The three bad
endings are told apart on purpose: a run that failed, a run somebody
stopped and a run that ran out of time are different news, and only
`FAILED` and `DELAYED` are painted red.

`AGE` is how long a live run has been going, or exactly how long a finished
one took. A live age is rounded to the minute once past one: a flap cell
takes about a second to turn, so a running seconds counter would never
settle. The line under the board spells the selected run out in full, since
the columns truncate hard on a narrow terminal.

The board sizes itself to the terminal: the workflow column takes up the
slack, and on a narrow one the actor, branch and time columns drop off
rather than running past the edge.

## Usage

Auth comes from `$GITHUB_TOKEN`, falling back to `gh auth token`.

```sh
clack --repo owner/name --repo owner/other          # repeatable, comma-separated
clack --repo owner/'*'                              # every repo of an owner
clack --demo                                        # simulated board, no API
```

| Flag         | Default             | Meaning                          |
| ------------ | ------------------- | -------------------------------- |
| `--repo`     | —                   | repo to watch (`OWNER/NAME`)     |
| `--interval` | `30s` (min `10s`)   | poll interval                    |
| `--rows`     | `12`                | rows shown at once (scroll for more) |
| `--per-repo` | `5`                 | runs fetched per repo per poll   |
| `--max-repos`| `20`                | repos an `OWNER/*` wildcard expands to |
| `--config`   | `~/.config/clack/config.yaml` | config file path       |
| `--bell`     | `true`              | ring the terminal on a failure   |
| `--on-fail`  | —                   | shell command to run on a failure |
| `--filter`   | `all`               | start in a view: `all`, `live`, `failed` |
| `--search`   | —                   | start with a search term         |
| `--once`     | —                   | print one board as plain text and exit |
| `--version`  | —                   | print the version and exit       |

Config file (all keys optional; flags win):

```yaml
repos:
  - owner/name
  - owner/other
interval: 20s
rows: 15
per_repo: 5
max_repos: 20
bell: true
on_fail: notify-send "CI failed" "$CLACK_REPO $CLACK_WORKFLOW"
```

Keys: `↑`/`k` `↓`/`j` select · `g`/`G` (or `home`/`end`) top/bottom ·
`pgup`/`pgdn` page · `f` cycle view · `/` search · `o`/`enter` open run in
browser · `r` poll now · `?` help · `esc` clear the filter · `q` quit.

Unknown keys in the config file are an error rather than a setting that
silently never applies, and a `--config` path that does not exist is only
ignored when it is the default one.

## Watching a whole owner

`OWNER/*` expands to that owner's repositories — an organisation or a user,
whichever exists — resolved once at startup. Quote it, or the shell will
expand it first:

```sh
clack --repo 'acme/*'
```

Archived repositories are left out, since they cannot produce another run,
and the rest are taken most recently pushed first, up to `--max-repos`.
That cap is arithmetic rather than politeness: the board spends one request
per repo per poll, so an owner with two hundred repositories would burn the
hourly budget in minutes. Sorting by last push means the cap keeps the
repositories somebody is actually working in.

## Filtering

Past a couple of repos the board fills with green you are not looking for.
`f` cycles between **ALL**, **LIVE** (anything still running or queued) and
**FAILED** (failed, cancelled, delayed or holding). `/` searches repo, workflow, branch
and actor, case-insensitively, as you type; `enter` keeps it, `esc` puts
back what was there before. `esc` on a filtered board clears the filter
rather than quitting, so backing out of a search cannot kill the board.

FAILED is deliberately broader than what rings the bell. A run somebody
cancelled is not worth interrupting them for, but it is exactly what they
want to see when they ask the board what went wrong.

Filtering is a view over what has already been fetched, so it costs no API
calls. `--rows` caps what is on screen, not what is kept: the selection
scrolls the board, and the footer says which rows you are looking at.

`--filter` and `--search` start the board in a view, which is also how they
pair with `--once`.

## One-shot output

`--once` polls once, prints a settled board as plain text and exits — for a
cron job, a status line, or a scrollback you want to keep:

```sh
clack --repo acme/web --once --filter failed
```

Nothing animates and nothing blinks, so no status can be caught mid-flap;
the key hints are left off; and the board is as tall as its contents. Width
comes from `$COLUMNS`, falling back to 100.

## Alerts

A board you have to watch is not much better than a browser tab, so clack
tells you when something breaks. When a run goes red it rings the terminal
bell (`--bell=false` to stop it) and runs `--on-fail`, if you set one:

```sh
clack --repo acme/web --on-fail 'notify-send "CI failed" "$CLACK_REPO $CLACK_WORKFLOW"'
```

The command goes to `sh -c` (`cmd /c` on Windows) and the run is passed in
the environment, never spliced into the command, so a branch name cannot
turn into shell: `CLACK_REPO`, `CLACK_WORKFLOW`, `CLACK_BRANCH`,
`CLACK_ACTOR`, `CLACK_STATUS`, `CLACK_CONCLUSION`, `CLACK_URL`,
`CLACK_RUN_ID`. Its output is discarded, it is given 30 seconds, and a
non-zero exit shows up in the footer.

What counts as worth interrupting you is exactly what the board paints red:
`FAILED` and `DELAYED`. A run somebody cancelled on purpose, or one that
was skipped, appears on the board but stays quiet. An ending GitHub has not
invented yet counts as a failure, on the grounds that a false alarm is
cheaper than a failure shown as `ENDED`. The first poll never alerts —
it is seeding the board from history, and everything red on it has already
happened. Each run is reported once, even if it drops off the bottom of the
board and comes back.

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

[![ci](https://github.com/jzbz/clack/actions/workflows/ci.yml/badge.svg)](https://github.com/jzbz/clack/actions/workflows/ci.yml)

Tests, vet and gofmt run on push across Linux, macOS and Windows, because
clack shells out differently on each: `xdg-open`/`open`/`rundll32` to show a
run, and `sh`/`cmd` to run an `--on-fail` hook.


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
