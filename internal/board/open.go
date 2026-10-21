package board

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// openURL is the browser launcher, swappable so a test can never put a real
// window on somebody's screen.
var openURL = openBrowser

// openBrowser opens url in the default browser without blocking. Output is
// discarded so the launcher cannot scribble over the TUI.
func openBrowser(url string) error {
	// The board only ever has GitHub's own html_url to hand, but the
	// launcher will open anything, so keep it to the web.
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return fmt.Errorf("refusing to open %q", url)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the launcher, or every run opened leaves a zombie behind for as
	// long as the board is up.
	go cmd.Wait()
	return nil
}
