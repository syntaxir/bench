package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// cleanups are the actions that undo what a capture set up (temporary worktrees, the work
// directory). They run once, in reverse order, when the capture returns or when the process is
// interrupted, so that neither a failed nor an interrupted run leaves a worktree registered in the
// checkout.
type cleanups struct {
	mu    sync.Mutex
	fns   []func()
	done  bool
	stop  chan struct{}
	ended sync.WaitGroup
}

// newCleanups returns an empty list that also runs on SIGINT and SIGTERM, after which the process
// exits with status 130.
func newCleanups() *cleanups {
	c := &cleanups{stop: make(chan struct{})}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	c.ended.Add(1)
	go func() {
		defer c.ended.Done()
		select {
		case s := <-sig:
			fmt.Fprintf(os.Stderr, "timed: %v: removing the temporary worktrees\n", s)
			c.runAll()
			os.Exit(130)
		case <-c.stop:
		}
		signal.Stop(sig)
	}()
	return c
}

func (c *cleanups) add(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fns = append(c.fns, fn)
}

// run runs the cleanups and stops watching for signals.
func (c *cleanups) run() {
	close(c.stop)
	c.ended.Wait()
	c.runAll()
}

func (c *cleanups) runAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	c.done = true
	for i := len(c.fns) - 1; i >= 0; i-- {
		c.fns[i]()
	}
}

// arms are the checkouts that a capture measures.
type arms struct {
	head   string // the checkout of head
	base   string // the checkout of base, or ""
	source string // where head comes from, for the headers: the repository and the revision
}

// isURL reports whether -main names a remote repository rather than a local directory.
func isURL(s string) bool {
	return strings.Contains(s, "://") || strings.HasPrefix(s, "git@")
}

// checkoutArms prepares the checkouts of head and, for a timed capture with -base, of base. A local
// checkout without -head is measured in place; any other revision is checked out with git worktree
// add into the work directory, and the worktree is removed by the cleanups.
func checkoutArms(o options, work string, timed bool, cl *cleanups) (arms, error) {
	var a arms
	repo, origin := o.main, o.main
	head := o.head
	if isURL(o.main) {
		var err error
		if repo, err = mirror(o.main); err != nil {
			return a, err
		}
		if head == "" {
			head = "main"
		}
	} else {
		abs, err := filepath.Abs(o.main)
		if err != nil {
			return a, err
		}
		if top := gitOutput(abs, "rev-parse", "--show-toplevel"); top == "unknown" {
			return a, fmt.Errorf("-main %s is not a Git checkout", o.main)
		}
		repo = abs
		// The headers name the repository by its public address, never by a local path.
		origin = gitOutput(abs, "remote", "get-url", "origin")
		if origin == "unknown" {
			origin = "a local repository"
		}
	}
	// Forget the worktrees of earlier runs whose directories are gone.
	execOut(repo, nil, "git", "worktree", "prune")
	add := func(name, rev string) (string, error) {
		dir := filepath.Join(work, name)
		if _, err := execOut(repo, nil, "git", "worktree", "add", "--detach", dir, rev); err != nil {
			return "", fmt.Errorf("checking out %s: %w", rev, err)
		}
		cl.add(func() {
			if _, err := execOut(repo, nil, "git", "worktree", "remove", "--force", dir); err != nil {
				fmt.Fprintln(os.Stderr, "timed: removing the worktree", dir+":", err)
			}
			execOut(repo, nil, "git", "worktree", "prune")
		})
		return dir, nil
	}
	if head == "" {
		a.head, a.source = repo, origin+", the working tree"
	} else {
		dir, err := add("head-src", head)
		if err != nil {
			return a, err
		}
		a.head, a.source = dir, origin+", "+head
	}
	if o.base != "" && timed {
		dir, err := add("base-src", o.base)
		if err != nil {
			return a, err
		}
		a.base = dir
	}
	return a, nil
}

// mirror returns a bare clone of a remote repository in the user's cache directory, created or
// brought up to date with all its branches and tags.
func mirror(url string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(url))
	dir := filepath.Join(cache, "syntaxir-bench", hex.EncodeToString(sum[:8])+".git")
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "cloning %s into %s\n", url, dir)
		if _, err := execOut("", nil, "git", "clone", "--bare", "--quiet", url, dir); err != nil {
			return "", err
		}
		return dir, nil
	}
	fmt.Fprintf(os.Stderr, "fetching %s into %s\n", url, dir)
	_, err = execOut(dir, nil, "git", "fetch", "--quiet", "--prune", "origin",
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	return dir, err
}
