// Command clack is a GitHub Actions departures board: a live, cross-repo view
// of workflow runs styled like an airport split-flap display.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"clack/internal/board"
	"clack/internal/ghapi"
)

const usage = `clack — GitHub Actions departures board

Watch workflow runs across repos on one live split-flap board.

Flags:`

// repoFlag collects -repo OWNER/NAME flags, repeatable and comma-separated.
type repoFlag []string

func (r *repoFlag) String() string { return strings.Join(*r, ",") }

func (r *repoFlag) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Count(part, "/") != 1 {
			return fmt.Errorf("repo %q: want OWNER/NAME", part)
		}
		*r = append(*r, part)
	}
	return nil
}

// fileConfig is the optional YAML config file.
type fileConfig struct {
	Repos    []string `yaml:"repos"`
	Interval string   `yaml:"interval"`
	Rows     int      `yaml:"rows"`
	PerRepo  int      `yaml:"per_repo"`
}

func main() {
	var repos repoFlag
	var configPath string
	var interval time.Duration
	var rows, perRepo int
	var demo bool
	flag.Var(&repos, "repo", "repo to watch (OWNER/NAME); repeatable, comma-separated")
	flag.StringVar(&configPath, "config", defaultConfigPath(), "config `file`")
	flag.DurationVar(&interval, "interval", 0, "poll interval (default 30s, min 10s)")
	flag.IntVar(&rows, "rows", 0, "max rows on the board (default 12)")
	flag.IntVar(&perRepo, "per-repo", 0, "runs fetched per repo (default 5)")
	flag.BoolVar(&demo, "demo", false, "run on simulated data, no API calls")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, usage)
		flag.PrintDefaults()
	}
	flag.Parse()

	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })

	var fc fileConfig
	if b, err := os.ReadFile(configPath); err == nil {
		if err := yaml.Unmarshal(b, &fc); err != nil {
			fatalf("config %s: %v", configPath, err)
		}
	}

	if len(repos) == 0 {
		repos = fc.Repos
	}
	if !set["interval"] && fc.Interval != "" {
		d, err := time.ParseDuration(fc.Interval)
		if err != nil {
			fatalf("config interval: %v", err)
		}
		interval = d
	}
	if interval == 0 {
		interval = 30 * time.Second
	}
	if !set["rows"] && fc.Rows != 0 {
		rows = fc.Rows
	}
	if rows == 0 {
		rows = 12
	}
	if !set["per-repo"] && fc.PerRepo != 0 {
		perRepo = fc.PerRepo
	}
	if perRepo == 0 {
		perRepo = 5
	}

	var fetch board.Fetcher
	if demo {
		fetch = board.Demo()
	} else {
		if len(repos) == 0 {
			flag.Usage()
			os.Exit(2)
		}
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			t, err := ghToken()
			if err != nil {
				fatalf("no GITHUB_TOKEN set and `gh auth token` failed: %v", err)
			}
			token = t
		}
		client := ghapi.NewClient(token, repos, perRepo)
		fetch = client.Fetch
	}

	p := tea.NewProgram(board.New(fetch, interval, rows), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fatalf("%v", err)
	}
}

func ghToken() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "clack", "config.yaml")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "clack: "+format+"\n", args...)
	os.Exit(1)
}
