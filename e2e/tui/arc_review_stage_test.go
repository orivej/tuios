package tuie2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuios/internal/review"
	"github.com/Gaurav-Gosain/tuitest"
)

// TestArcReviewStageHunks uses a private real Arc mount. It checks both index
// views, partial staging, reverse application, new/deleted files, CRLF and a
// missing final newline. Every write must leave the working bytes unchanged.
func TestArcReviewStageHunks(t *testing.T) {
	base, source, arcRun := arcFixture(t)
	var metadata struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal([]byte(arcRun(source, "info", "--json")), &metadata); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "stage-mount")
	arcRun(os.TempDir(), "mount", "-r", metadata.Repository, "-m", root)
	t.Cleanup(func() {
		_, _ = tuiosCLI(t, base, "kill-server")
		cmd := exec.Command("arc", "unmount", "--force", "--forget", root)
		cmd.Dir = os.TempDir()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("unmount: %v: %s", err, out)
		}
	})
	arcRun(root, "checkout", "-b", fmt.Sprintf("tuios-stage-%d", time.Now().UnixNano()))
	const name = "tuios-stage-test.txt"
	var lines []string
	for i := range 30 {
		lines = append(lines, fmt.Sprintf("line %02d", i))
	}
	original := strings.Join(lines, "\r\n") // no final newline
	file := filepath.Join(root, name)
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	arcRun(root, "add", "--", name)
	arcRun(root, "commit", "-m", "test: Arc hunk staging", "--no-verify", "--skip-hook", "prepare-commit-msg")
	work := strings.Replace(original, "line 01", "FIRST", 1)
	work = strings.Replace(work, "line 28", "LAST", 1)
	if err := os.WriteFile(file, []byte(work), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := tuiosCLIIn(t, base, root, "new", "arc-stage", "--detach"); err != nil {
		t.Fatalf("new: %v: %s", err, out)
	}
	b := startBridgeWith(t, base, bridgeOpts{args: []string{"--session", "arc-stage"}, cols: 120, rows: 40, keepDaemon: true, name: "arc-stage"})
	var log strings.Builder
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(artifactDir(t), "arc-staging.txt"), []byte(log.String()), 0o644); err != nil {
			t.Error(err)
		}
	})
	diff := func(mode, path string) []review.File {
		t.Helper()
		res := b.verb("review-diff", map[string]any{"session": "arc-stage", "index": mode, "paths": []string{path}})
		if !res.OK {
			t.Fatalf("index %s: %s %s", mode, res.Code, res.Error)
		}
		var d struct {
			Files []review.File `json:"files"`
		}
		if err := json.Unmarshal(res.Result, &d); err != nil {
			t.Fatal(err)
		}
		return d.Files
	}
	stage := func(action, path string, h review.Hunk, selected []int) {
		t.Helper()
		params := map[string]any{"session": "arc-stage", "action": action, "path": path, "hunk": h.Header}
		if selected != nil {
			params["lines"] = selected
		}
		res := b.verb("review-stage", params)
		fmt.Fprintf(&log, "%s %s %s: %s\n", action, path, h.Header, res.Result)
		if !res.OK {
			t.Fatalf("%s: %s %s", action, res.Code, res.Error)
		}
		if got := readFile(t, file); got != work {
			t.Fatalf("working file changed: %q", got)
		}
	}
	index := func(want string) {
		t.Helper()
		got := arcRun(root, "show", ":"+name)
		fmt.Fprintf(&log, "index: %q\n", got)
		if got != want {
			t.Fatalf("index = %q, want %q", got, want)
		}
	}
	u := diff("unstaged", name)
	if len(u) != 1 || len(u[0].Hunks) != 2 {
		t.Fatalf("want two unstaged hunks: %+v", u)
	}
	// Exercise the person's cursor action as well as the protocol below.
	term := attachIn(t, base, "arc-stage", startOpts{})
	sendKeys(t, term, tuitest.Ctrl('b'), "v")
	waitScreen(t, term, "Arc review did not open", "Review", name, "FIRST", "a stage")
	sendKeys(t, term, "u")
	waitScreen(t, term, "Arc uncommitted review did not open", "uncommitted only", "- line 01")
	sendKeys(t, term, "a")
	waitScreen(t, term, "Arc hunk did not stage", "Hunk staged", "d unstage")
	index(strings.Replace(original, "line 01", "FIRST", 1))
	saveArtifact(t, term, artifactDir(t), "arc-review-staged-hunk")
	sendKeys(t, term, "d")
	waitScreen(t, term, "Arc hunk did not unstage", "Hunk unstaged")
	index(original)
	sendKeys(t, term, "esc")
	stage("stage", name, u[0].Hunks[0], nil)
	first := strings.Replace(original, "line 01", "FIRST", 1)
	index(first)
	u = diff("unstaged", name)
	var add int
	for i, line := range u[0].Hunks[0].Lines {
		if line.Op == review.OpAdd {
			add = i
		}
	}
	stage("stage", name, u[0].Hunks[0], []int{add})
	index(strings.Replace(first, "line 28", "line 28\r\nLAST", 1))
	s := diff("staged", name)
	stage("unstage", name, s[0].Hunks[len(s[0].Hunks)-1], nil)
	index(first)
	s = diff("staged", name)
	stage("unstage", name, s[0].Hunks[0], nil)
	index(original)
	const fresh = "tuios-stage-new.txt"
	if err := os.WriteFile(filepath.Join(root, fresh), []byte("new\nlast"), 0o644); err != nil {
		t.Fatal(err)
	}
	u = diff("unstaged", fresh)
	stage("stage", fresh, u[0].Hunks[0], nil)
	if got := arcRun(root, "show", ":"+fresh); got != "new\nlast" {
		t.Fatalf("new index: %q", got)
	}
	s = diff("staged", fresh)
	stage("unstage", fresh, s[0].Hunks[0], nil)
	if got := diff("staged", fresh); len(got) != 0 {
		t.Fatalf("new file still staged: %+v", got)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	// Deletion is checked separately because the working file is absent.
	u = diff("unstaged", name)
	res := b.verb("review-stage", map[string]any{"session": "arc-stage", "action": "stage", "path": name, "hunk": u[0].Hunks[0].Header})
	if !res.OK {
		t.Fatalf("stage deletion: %s", res.Error)
	}
	s = diff("staged", name)
	res = b.verb("review-stage", map[string]any{"session": "arc-stage", "action": "unstage", "path": name, "hunk": s[0].Hunks[0].Header})
	if !res.OK {
		t.Fatalf("unstage deletion: %s", res.Error)
	}
	index(original)
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("unstaging restored working file: %v", err)
	}
}
