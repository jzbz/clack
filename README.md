# clack

A GitHub Actions **departures board**: a live, cross-repo view of workflow
runs styled like an airport split-flap display. `gh run watch` follows one
run; clack shows everything at once.

```sh
go install github.com/jzbz/clack@latest
```

```
CLACK · GITHUB ACTIONS DEPARTURES                                              20:17:14
TIME  REPO           WORKFLOW     BRANCH       ACTOR       STATUS     AGE
20:13 ACME/WEB       CI           MAIN         OCTOCAT     BOARDING   4M
20:03 ACME/INFRA     TERRAFORM    MAIN         OCTOCAT     DEPARTED   7M
19:51 ACME/WEB       LINT         FIX/CSS      MONA        CANCELLED  8M
```

Status vocabulary: in progress → `BOARDING` (blinks), success → `DEPARTED`,
failure/cancelled → `CANCELLED`, queued → `QUEUED`, waiting → `WAITING`,
skipped → `SKIPPED`, action required → `HOLD`.

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

Keys: `↑`/`k` `↓`/`j` select · `o`/`enter` open run in browser ·
`r` poll now · `?` help · `q` quit.

## Rate limits

One API request per repo per poll. The board reads `X-RateLimit-*` headers,
shows the remaining budget in the footer, waits out an exhausted limit until
its reset time, and backs off exponentially (to 5 min) on consecutive errors.

## Develop

```sh
go test ./...          # unit tests
CLACK_LIVE=1 go test ./internal/ghapi/    # one real API call
CLACK_SNAPSHOT=$PWD/frames go test ./internal/board/ -run TestSnapshot
```
