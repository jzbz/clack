// Command clack is a GitHub Actions departures board: a live, cross-repo view
// of workflow runs styled like an airport split-flap display.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"gopkg.in/yaml.v3"

	"github.com/jzbz/clack/internal/board"
	"github.com/jzbz/clack/internal/ghapi"
)

const usage = `clack — GitHub Actions departures board

Watch workflow runs across repos on one live split-flap board.

Flags:`

// Defaults for anything neither a flag nor the config file sets.
const (
	defaultInterval  = 30 * time.Second
	defaultRows      = 12
	defaultPerRepo   = 5
	defaultMaxRepos  = 20
	defaultOnceWidth = 100
	onceTimeout      = 60 * time.Second
	expandTimeout    = 30 * time.Second
)

var (
	// errUsage means the flags were wrong and usage has already been printed.
	errUsage = errors.New("usage")
	// errDone means the work is finished and there is nothing to report.
	errDone = errors.New("done")
)

func main() {
	switch err := run(os.Args[1:]); {
	case err == nil, errors.Is(err, errDone):
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
			return fmt.Errorf("repo %q: want OWNER/NAME or OWNER/*", part)
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
	MaxRepos int      `yaml:"max_repos"`
	Bell     *bool    `yaml:"bell"` // pointer so "unset" differs from false
	OnFail   string   `yaml:"on_fail"`
}

// options are the settings clack runs with, once flags, the config file and
// the defaults above have been reconciled.
type options struct {
	repos    []string
	interval time.Duration
	rows     int
	perRepo  int
	maxRepos int
	demo     bool
	bell     bool
	onFail   string
	once     bool
	filter   string
	search   string
}

// resolve turns command-line arguments into options. Flags win over the
// config file, which wins over the defaults.
func resolve(args []string) (options, error) {
	var (
		opt        options
		repos      repoFlag
		version    bool
		configPath = defaultConfigPath()
	)
	flags := flag.NewFlagSet("clack", flag.ContinueOnError)
	flags.Var(&repos, "repo", "repo to watch (OWNER/NAME); repeatable, comma-separated")
	flags.StringVar(&configPath, "config", configPath, "config `file`")
	flags.DurationVar(&opt.interval, "interval", 0, "poll interval (default 30s, min 10s)")
	flags.IntVar(&opt.rows, "rows", 0, "max rows on the board (default 12)")
	flags.IntVar(&opt.perRepo, "per-repo", 0, "runs fetched per repo (default 5)")
	flags.IntVar(&opt.maxRepos, "max-repos", 0,
		"repos an OWNER/* wildcard may expand to (default 20)")
	flags.BoolVar(&opt.demo, "demo", false, "run on simulated data, no API calls")
	flags.BoolVar(&opt.bell, "bell", true, "ring the terminal when a run fails")
	flags.StringVar(&opt.onFail, "on-fail", "", "shell `command` to run when a run fails")
	flags.BoolVar(&opt.once, "once", false, "print one board as plain text and exit")
	flags.StringVar(&opt.filter, "filter", "", "start in a `view`: all, live or failed")
	flags.StringVar(&opt.search, "search", "", "start with a search `term`")
	flags.BoolVar(&version, "version", false, "print the version and exit")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), usage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return opt, errUsage // flag has already reported it
	}
	if version {
		fmt.Println(versionString())
		return opt, errDone
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
	if !given["max-repos"] {
		opt.maxRepos = fc.MaxRepos
	}
	if !given["bell"] && fc.Bell != nil {
		opt.bell = *fc.Bell
	}
	if !given["on-fail"] && fc.OnFail != "" {
		opt.onFail = fc.OnFail
	}

	if _, err := parseFilter(opt.filter); err != nil {
		return opt, err
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
	if opt.maxRepos <= 0 {
		opt.maxRepos = defaultMaxRepos
	}
	return opt, nil
}

func run(args []string) error {
	opt, err := resolve(args)
	if err != nil {
		return err
	}

	var (
		fetch board.Fetcher
		rerun board.Rerunner // nil in demo mode: there is nothing to re-run
	)
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
		client := ghapi.NewClient(token, opt.repos, opt.perRepo)
		// A wildcard is resolved once, here, so the board never has to
		// wonder what it is watching.
		ctx, cancel := context.WithTimeout(context.Background(), expandTimeout)
		err = client.ExpandWildcards(ctx, opt.maxRepos)
		cancel()
		if err != nil {
			return err
		}
		fetch = client.Fetch
		rerun = client.Rerun
	}

	mode, err := parseFilter(opt.filter)
	if err != nil {
		return err
	}
	bopt := board.Options{
		Interval: opt.interval,
		MaxRows:  opt.rows,
		Bell:     opt.bell,
		OnFail:   opt.onFail,
		View:     mode,
		Search:   opt.search,
		Rerun:    rerun,
	}

	if opt.once {
		ctx, cancel := context.WithTimeout(context.Background(), onceTimeout)
		defer cancel()
		// No colour: a printed board is usually going somewhere that
		// would only show the escapes.
		lipgloss.SetColorProfile(termenv.Ascii)
		out, err := board.Once(ctx, fetch, bopt, onceWidth())
		fmt.Print(out)
		return err
	}

	p := tea.NewProgram(board.New(fetch, bopt), tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// parseFilter turns the --filter name into a view.
func parseFilter(name string) (board.View, error) {
	switch strings.ToLower(name) {
	case "", "all":
		return board.ViewAll, nil
	case "live":
		return board.ViewLive, nil
	case "failed":
		return board.ViewFailed, nil
	default:
		return board.ViewAll, fmt.Errorf("filter %q: want all, live or failed", name)
	}
}

// onceWidth is how wide a printed board should be. There may be no terminal
// to ask, so COLUMNS gets the final say and 100 is the fallback.
func onceWidth() int {
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n >= 40 {
		return n
	}
	return defaultOnceWidth
}

// versionString describes the binary. `go install` records a module
// version; a local build records only the commit it came from, so fall back
// to that rather than printing a bare "(devel)".
func versionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "clack (unknown version)"
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" {
		v = "(devel)"
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				v = "(devel " + s.Value[:12] + ")"
			}
		}
	}
	return "clack " + v
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
