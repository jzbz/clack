// Command clack is a GitHub Actions departures board: a live, cross-repo view
// of workflow runs styled like an airport split-flap display.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/jzbz/clack/internal/board"
	"github.com/jzbz/clack/internal/ghapi"
)

const usage = `clack — GitHub Actions departures board

Watch workflow runs across repos on one live split-flap board.

Flags:`

// Defaults for anything neither a flag nor the config file sets.
const (
	defaultInterval = 30 * time.Second
	defaultRows     = 12
	defaultPerRepo  = 5
)

// errUsage means the flags were wrong and usage has already been printed.
var errUsage = errors.New("usage")

func main() {
	switch err := run(os.Args[1:]); {
	case err == nil:
	case errors.Is(err, errUsage):
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "clack: %v\n", err)
		os.Exit(1)
	}
}

// repoFlag collects -repo OWNER/NAME flags, repeatable and comma-separated.
type repoFlag []string

func (r *repoFlag) String() string { return strings.Join(*r, ",") }

func (r *repoFlag) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Count(part, "/") != 1 || strings.HasPrefix(part, "/") || strings.HasSuffix(part, "/") {
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

// options are the settings clack runs with, once flags, the config file and
// the defaults above have been reconciled.
type options struct {
	repos    []string
	interval time.Duration
	rows     int
	perRepo  int
	demo     bool
}

// resolve turns command-line arguments into options. Flags win over the
// config file, which wins over the defaults.
func resolve(args []string) (options, error) {
	var (
		opt        options
		repos      repoFlag
		configPath = defaultConfigPath()
	)
	flags := flag.NewFlagSet("clack", flag.ContinueOnError)
	flags.Var(&repos, "repo", "repo to watch (OWNER/NAME); repeatable, comma-separated")
	flags.StringVar(&configPath, "config", configPath, "config `file`")
	flags.DurationVar(&opt.interval, "interval", 0, "poll interval (default 30s, min 10s)")
	flags.IntVar(&opt.rows, "rows", 0, "max rows on the board (default 12)")
	flags.IntVar(&opt.perRepo, "per-repo", 0, "runs fetched per repo (default 5)")
	flags.BoolVar(&opt.demo, "demo", false, "run on simulated data, no API calls")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), usage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return opt, errUsage // flag has already reported it
	}
	given := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { given[f.Name] = true })

	fc, err := loadConfig(configPath, given["config"])
	if err != nil {
		return opt, err
	}
	if len(repos) == 0 {
		// Checked the same way as a repo typed on the command line.
		for _, r := range fc.Repos {
			if err := repos.Set(r); err != nil {
				return opt, fmt.Errorf("%s: %w", configPath, err)
			}
		}
	}
	if !given["interval"] && fc.Interval != "" {
		if opt.interval, err = time.ParseDuration(fc.Interval); err != nil {
			return opt, fmt.Errorf("%s: interval: %w", configPath, err)
		}
	}
	if !given["rows"] {
		opt.rows = fc.Rows
	}
	if !given["per-repo"] {
		opt.perRepo = fc.PerRepo
	}

	opt.repos = repos
	if opt.interval <= 0 {
		opt.interval = defaultInterval
	}
	if opt.rows <= 0 {
		opt.rows = defaultRows
	}
	if opt.perRepo <= 0 {
		opt.perRepo = defaultPerRepo
	}
	return opt, nil
}

func run(args []string) error {
	opt, err := resolve(args)
	if err != nil {
		return err
	}

	var fetch board.Fetcher
	switch {
	case opt.demo:
		fetch = board.Demo()
	case len(opt.repos) == 0:
		return errors.New("nothing to watch: pass -repo OWNER/NAME, " +
			"list repos in the config file, or try -demo")
	default:
		token, err := githubToken()
		if err != nil {
			return err
		}
		fetch = ghapi.NewClient(token, opt.repos, opt.perRepo).Fetch
	}

	p := tea.NewProgram(board.New(fetch, opt.interval, opt.rows), tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// loadConfig reads the optional YAML config. A missing file is only an error
// when the user named it themselves.
func loadConfig(path string, explicit bool) (fileConfig, error) {
	var fc fileConfig
	if path == "" {
		return fc, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			return fc, nil
		}
		return fc, err
	}
	// KnownFields turns a mistyped key into an error rather than a setting
	// that silently never applies.
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil && !errors.Is(err, io.EOF) {
		return fc, fmt.Errorf("%s: %w", path, err)
	}
	return fc, nil
}

// githubToken takes the token from the environment, falling back to gh's.
func githubToken() (string, error) {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t, nil
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
			err = errors.New(string(bytes.TrimSpace(ee.Stderr)))
		}
		return "", fmt.Errorf("no GITHUB_TOKEN set and `gh auth token` failed: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("no GITHUB_TOKEN set and `gh auth token` printed nothing")
	}
	return token, nil
}

func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "clack", "config.yaml")
}
