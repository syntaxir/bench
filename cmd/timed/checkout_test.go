package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes a repository with one commit and an origin address, and returns its directory.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "one"},
		{"remote", "add", "origin", "https://example.com/syntaxir.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// worktrees lists the worktrees that a repository has registered, its own included.
func worktrees(t *testing.T, repo string) []string {
	t.Helper()
	var dirs []string
	for _, l := range strings.Split(gitOutput(repo, "worktree", "list", "--porcelain"), "\n") {
		if d, ok := strings.CutPrefix(l, "worktree "); ok {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// TestCheckoutArmsRemovesItsWorktrees checks that the worktrees of head and base are registered
// while a capture runs and gone, directories and registrations, after its cleanups, and that the
// headers name the repository by its address, not by its local path.
func TestCheckoutArmsRemovesItsWorktrees(t *testing.T) {
	repo := gitRepo(t)
	work := t.TempDir()
	cl := newCleanups()
	a, err := checkoutArms(options{main: repo, head: "main", base: "main"}, work, true, cl)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(worktrees(t, repo)); got != 3 {
		t.Errorf("%d worktrees registered during the capture, want 3", got)
	}
	if a.head == repo || a.base == "" {
		t.Errorf("arms %+v: want head and base in worktrees", a)
	}
	if a.source != "https://example.com/syntaxir.git, main" {
		t.Errorf("source = %q", a.source)
	}
	cl.run()
	if got := worktrees(t, repo); len(got) != 1 {
		t.Errorf("worktrees after the cleanups: %v", got)
	}
	for _, d := range []string{a.head, a.base} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s still exists (%v)", d, err)
		}
	}
}

// TestCheckoutArmsCleansUpAfterAFailure checks that a run that fails after checking out head, here
// at a base revision that does not exist, leaves no worktree registered once its cleanups run.
func TestCheckoutArmsCleansUpAfterAFailure(t *testing.T) {
	repo := gitRepo(t)
	cl := newCleanups()
	if _, err := checkoutArms(options{main: repo, head: "main", base: "no-such-revision"}, t.TempDir(), true, cl); err == nil {
		t.Fatal("a missing base revision was checked out")
	}
	cl.run()
	if got := worktrees(t, repo); len(got) != 1 {
		t.Errorf("worktrees after a failed run: %v", got)
	}
}

// TestCheckoutArmsMeasuresALocalCheckoutInPlace checks that without -head a local checkout is head
// itself, and that a capture without timed parts checks no base out.
func TestCheckoutArmsMeasuresALocalCheckoutInPlace(t *testing.T) {
	repo := gitRepo(t)
	cl := newCleanups()
	defer cl.run()
	a, err := checkoutArms(options{main: repo, base: "main"}, t.TempDir(), false, cl)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(repo)
	if a.head != want || a.base != "" || a.source != "https://example.com/syntaxir.git, the working tree" {
		t.Errorf("arms %+v", a)
	}
	if _, err := checkoutArms(options{main: t.TempDir()}, t.TempDir(), false, cl); err == nil {
		t.Error("a directory outside Git was accepted as a checkout")
	}
}

func TestIsURL(t *testing.T) {
	for s, want := range map[string]bool{
		"https://github.com/syntaxir/syntaxir.git": true,
		"git@github.com:syntaxir/syntaxir.git":     true,
		"../syntaxir":                              false,
		"/src/syntaxir":                            false,
	} {
		if isURL(s) != want {
			t.Errorf("isURL(%q) = %v", s, !want)
		}
	}
}
