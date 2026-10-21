package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoFlag(t *testing.T) {
	var r repoFlag
	if err := r.Set("acme/web, acme/api ,,"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := r.Set("acme/infra"); err != nil {
		t.Fatalf("Set again: %v", err)
	}
	if got, want := strings.Join(r, "|"), "acme/web|acme/api|acme/infra"; got != want {
		t.Errorf("repos = %q, want %q", got, want)
	}
	for _, bad := range []string{"web", "a/b/c", "/web", "acme/"} {
		var r repoFlag
		if err := r.Set(bad); err == nil {
			t.Errorf("Set(%q) was accepted as OWNER/NAME", bad)
		}
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig(t *testing.T) {
	path := writeConfig(t, "repos:\n  - acme/web\n  - acme/api\ninterval: 20s\nrows: 15\nper_repo: 7\n")
	fc, err := loadConfig(path, true)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(fc.Repos) != 2 || fc.Interval != "20s" || fc.Rows != 15 || fc.PerRepo != 7 {
		t.Errorf("config = %+v", fc)
	}
}

func TestLoadConfigEmptyFile(t *testing.T) {
	if _, err := loadConfig(writeConfig(t, ""), true); err != nil {
		t.Errorf("an empty config is not an error, got %v", err)
	}
}

// A typo in a config key used to be silently ignored, leaving the user with
// a setting that never applied.
func TestLoadConfigRejectsUnknownKey(t *testing.T) {
	_, err := loadConfig(writeConfig(t, "rows: 4\nper-repo: 7\n"), true)
	if err == nil {
		t.Fatal("a mistyped key was accepted")
	}
	if !strings.Contains(err.Error(), "per-repo") {
		t.Errorf("error %q does not name the offending key", err)
	}
}

// A missing config is normal at the default path and a mistake when asked for.
func TestLoadConfigMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := loadConfig(missing, false); err != nil {
		t.Errorf("default path that does not exist: %v", err)
	}
	if _, err := loadConfig(missing, true); err == nil {
		t.Error("a config named on the command line was missing, with no complaint")
	}
	if _, err := loadConfig("", false); err != nil {
		t.Errorf("unknown config dir: %v", err)
	}
}

// Flags beat the config file, the config file beats the defaults.
func TestRunPrecedence(t *testing.T) {
	path := writeConfig(t, "repos:\n  - acme/api\ninterval: 20s\nrows: 15\nper_repo: 7\n")
	cases := []struct {
		name     string
		args     []string
		interval string
		rows     int
		perRepo  int
		repos    string
	}{
		{"config only", nil, "20s", 15, 7, "acme/api"},
		{"flags win", []string{"-interval", "45s", "-rows", "3", "-per-repo", "2", "-repo", "acme/web"},
			"45s", 3, 2, "acme/web"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolve(append([]string{"-config", path}, c.args...))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.interval.String() != c.interval {
				t.Errorf("interval = %v, want %v", got.interval, c.interval)
			}
			if got.rows != c.rows || got.perRepo != c.perRepo {
				t.Errorf("rows = %d, perRepo = %d, want %d and %d", got.rows, got.perRepo, c.rows, c.perRepo)
			}
			if strings.Join(got.repos, "|") != c.repos {
				t.Errorf("repos = %v, want %v", got.repos, c.repos)
			}
		})
	}
}

func TestRunDefaults(t *testing.T) {
	if _, err := resolve([]string{"-config", filepath.Join(t.TempDir(), "absent.yaml"), "-repo", "acme/web"}); err == nil {
		t.Fatal("a config named on the command line was missing, with no complaint")
	}

	got, err := resolve([]string{"-repo", "acme/web", "-config", ""})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.interval != defaultInterval || got.rows != defaultRows || got.perRepo != defaultPerRepo {
		t.Errorf("defaults not applied: %+v", got)
	}
}

// A repo typed into the config file is checked just like one typed as a flag.
func TestRunRejectsBadConfigRepo(t *testing.T) {
	path := writeConfig(t, "repos:\n  - not-a-repo\n")
	if _, err := resolve([]string{"-config", path}); err == nil {
		t.Fatal("a malformed repo in the config file was accepted")
	}
}

// The bell is on unless something turns it off, and "off" in the config
// file has to be distinguishable from "not mentioned".
func TestResolveAlertOptions(t *testing.T) {
	plain := writeConfig(t, "repos:\n  - acme/web\n")
	cases := []struct {
		name   string
		config string
		args   []string
		bell   bool
		onFail string
	}{
		{"default", plain, nil, true, ""},
		{"config turns the bell off", "repos:\n  - acme/web\nbell: false\n", nil, false, ""},
		{"config sets a hook", "repos:\n  - acme/web\non_fail: notify-send ci\n", nil, true, "notify-send ci"},
		{"flag beats config", "repos:\n  - acme/web\nbell: false\n", []string{"-bell"}, true, ""},
		{"flag turns the bell off", plain, []string{"-bell=false"}, false, ""},
		{"flag hook beats config", "repos:\n  - acme/web\non_fail: from-config\n",
			[]string{"-on-fail", "from-flag"}, true, "from-flag"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := c.config
			if !strings.HasPrefix(path, "/") {
				path = writeConfig(t, c.config)
			}
			got, err := resolve(append([]string{"-config", path}, c.args...))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.bell != c.bell {
				t.Errorf("bell = %v, want %v", got.bell, c.bell)
			}
			if got.onFail != c.onFail {
				t.Errorf("onFail = %q, want %q", got.onFail, c.onFail)
			}
		})
	}
}
