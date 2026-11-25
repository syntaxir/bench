package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A results release publishes RESULTS.md and figures.json of a commit of this repository as the
// assets of a GitHub release tagged results-vN. SyntaxIR pins one: its site renders the release's
// RESULTS.md as its benchmarks page after checking it against the pinned digest, and its documents
// quote the figures of the copy of figures.json that pin writes next to the pin.

// releaseAssets are the assets of a results release.
var releaseAssets = []string{"RESULTS.md", "figures.json"}

// releaseTag is the form of the tag of a results release.
var releaseTag = regexp.MustCompile(`^results-v[1-9][0-9]*$`)

// defaultReleaseRepository is the repository of the releases.
const defaultReleaseRepository = "https://github.com/syntaxir/bench"

// Paths of the pin and of the copy of the figures in a SyntaxIR checkout.
const (
	pinFile     = "site/benchmarks.json"
	figuresCopy = "site/figures.json"
)

// pin is the format of site/benchmarks.json, in the form of SyntaxIR's site/playground-wasm.json.
type pin struct {
	Repository    string `json:"repository"`
	Tag           string `json:"tag"`
	ResultsSHA256 string `json:"resultsSHA256"`
	FiguresSHA256 string `json:"figuresSHA256"`
}

// releaseCmd publishes RESULTS.md and figures.json of HEAD as a results release, with the gh
// command. It refuses unless the tree is clean, both files match results/, and HEAD is the main
// branch of the remote, so that the release names a commit that anyone can check out.
func releaseCmd(args []string) error {
	fl := flag.NewFlagSet("release", flag.ExitOnError)
	tag := fl.String("tag", "", "the tag of the release: results-vN (required)")
	repo := fl.String("repo", defaultReleaseRepository, "the repository of the release")
	fl.Parse(args)
	if !releaseTag.MatchString(*tag) {
		return fmt.Errorf("-tag %q: want results-vN", *tag)
	}
	harness, err := harnessRoot()
	if err != nil {
		return err
	}
	if out := gitOutput(harness, "status", "--porcelain"); out != "" {
		return fmt.Errorf("the tree has uncommitted changes:\n%s", out)
	}
	doc, figures, err := renderResults(filepath.Join(harness, "results"))
	if err != nil {
		return err
	}
	for name, want := range map[string][]byte{"RESULTS.md": doc, "figures.json": figures} {
		got, err := os.ReadFile(filepath.Join(harness, name))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("%s does not match results/: run go run ./cmd/timed render and commit", name)
		}
	}
	head := gitOutput(harness, "rev-parse", "HEAD")
	remote, err := execOut(harness, nil, "git", "ls-remote", "origin", "refs/heads/main")
	if err != nil {
		return err
	}
	if f := strings.Fields(remote); len(f) == 0 || f[0] != head {
		return fmt.Errorf("HEAD %s is not the main branch of origin (%s): push it first", head, strings.TrimSpace(remote))
	}
	values, err := decodeFigures(figures)
	if err != nil {
		return err
	}
	notes := fmt.Sprintf("The timed results of SyntaxIR commit %s, measured on %s.", values["run-commit"], values["run-date"])
	gh := []string{"release", "create", *tag, "--repo", *repo, "--target", head, "--title", *tag, "--notes", notes}
	for _, a := range releaseAssets {
		gh = append(gh, filepath.Join(harness, a))
	}
	out, err := execOut(harness, nil, "gh", gh...)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// pinCmd pins a results release in a SyntaxIR checkout: it downloads the release's assets, writes
// site/benchmarks.json with their digests, writes the release's figures.json to site/figures.json and
// rewrites the marked figures of the checkout's documents with its values. The checkout's changes
// are then one commit.
func pinCmd(args []string) error {
	fl := flag.NewFlagSet("pin", flag.ExitOnError)
	mainDir := fl.String("main", "", "the SyntaxIR checkout to pin the release in (required)")
	tag := fl.String("tag", "", "the tag of the release: results-vN (required)")
	repo := fl.String("repo", defaultReleaseRepository, "the repository of the release")
	fl.Parse(args)
	if *mainDir == "" {
		return errors.New("-main names the SyntaxIR checkout to pin the release in")
	}
	if !releaseTag.MatchString(*tag) {
		return fmt.Errorf("-tag %q: want results-vN", *tag)
	}
	assets := map[string][]byte{}
	for _, a := range releaseAssets {
		data, err := download(strings.TrimSuffix(*repo, "/") + "/releases/download/" + *tag + "/" + a)
		if err != nil {
			return err
		}
		assets[a] = data
	}
	values, err := decodeFigures(assets["figures.json"])
	if err != nil {
		return err
	}
	p := pin{Repository: *repo, Tag: *tag, ResultsSHA256: sha256Hex(assets["RESULTS.md"]), FiguresSHA256: sha256Hex(assets["figures.json"])}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	for path, content := range map[string][]byte{pinFile: append(data, '\n'), figuresCopy: assets["figures.json"]} {
		full := filepath.Join(*mainDir, filepath.FromSlash(path))
		if err := os.WriteFile(full, content, 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", full)
	}
	stale, err := applyFigures(*mainDir, values, true)
	if err != nil {
		return err
	}
	for _, path := range stale {
		fmt.Println("updated the figures in", path)
	}
	return nil
}

func sha256Hex(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// download returns the body of a URL.
func download(url string) ([]byte, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}
