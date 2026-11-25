// Package probe builds the sxir command of a SyntaxIR checkout, generates parsers with it, and
// builds and measures programs that use them, through the checkout's command and public packages
// only. cmd/timed and cmd/profilematrix share it.
package probe

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// ExecOut runs a command in dir, with extra environment variables, and returns its standard output.
func ExecOut(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(slices.Clone(args), " "), err, stderr.String())
	}
	return string(out), nil
}

// BuildSXIR builds the sxir command of the checkout at root into the work directory.
func BuildSXIR(root, work string) (string, error) {
	out := filepath.Join(work, "sxir")
	if _, err := ExecOut(root, []string{"GOFLAGS=" + os.Getenv("GOFLAGS")}, "go", "build", "-trimpath", "-o", out, "./cmd/sxir"); err != nil {
		return "", err
	}
	return out, nil
}

// TSArgs adapts the options of a Go generation to TypeScript: it drops the Go-only options
// (-module, -lib and -o with their values, -nodoc and -typed-only).
func TSArgs(gen []string) []string {
	var out []string
	for i := 0; i < len(gen); i++ {
		switch gen[i] {
		case "-module", "-lib", "-o":
			i++
		case "-nodoc", "-typed-only":
		default:
			out = append(out, gen[i])
		}
	}
	return out
}

// GenerateGo generates the Go parser of the grammar at path grammar of the checkout into dir as the
// package sizeprobe/gen, with the options gen followed by extra.
func GenerateGo(root, sxirBin, dir, grammar string, gen []string, extra ...string) error {
	args := append([]string{"gen", "-g", filepath.Join(root, filepath.FromSlash(grammar)), "-module", "sizeprobe/gen"}, gen...)
	args = append(args, extra...)
	_, err := ExecOut(root, nil, sxirBin, append(args, "-o", dir)...)
	return err
}

// GenerateTS generates the TypeScript parser of the grammar into dir, with the options gen adapted
// by TSArgs followed by extra.
func GenerateTS(root, sxirBin, dir, grammar string, gen []string, extra ...string) error {
	args := append([]string{"gen", "-lang", "ts", "-g", filepath.Join(root, filepath.FromSlash(grammar))}, TSArgs(gen)...)
	args = append(args, extra...)
	_, err := ExecOut(root, nil, sxirBin, append(args, "-o", dir)...)
	return err
}

// SizeOfFile returns the size of a file and of its gzip -9 compression.
func SizeOfFile(path string) (raw, gz int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := w.Write(data); err != nil {
		return 0, 0, err
	}
	if err := w.Close(); err != nil {
		return 0, 0, err
	}
	return len(data), buf.Len(), nil
}

// SizeMain is a program that calls ParseValue of the parser in the package sizeprobe/gen.
const SizeMain = `package main

import (
	"os"

	p "sizeprobe/gen"
)

func main() {
	if _, err := p.ParseValue(os.Args[1]); err != nil {
		os.Exit(1)
	}
}
`

// BuildProgram builds a program with the source main, which imports the parser generated into genDir as
// sizeprobe/gen, in a module of its own in dir, and returns the path of the stripped binary (built with
// -trimpath) and its size. The module replaces syntaxir.dev/sxir, whose public packages the generated
// parser imports, by the checkout at root.
//
// buildFlags are added to the go build command.
func BuildProgram(root, dir, genDir, main string, buildFlags ...string) (path string, size int, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	mod := fmt.Sprintf(`module sizeprobe

go 1.27.1

require %[1]s v0.0.0

replace %[1]s => %[2]s
`, "syntaxir.dev/sxir", root)
	if err := os.CopyFS(filepath.Join(dir, "gen"), os.DirFS(genDir)); err != nil {
		return "", 0, err
	}
	for file, content := range map[string]string{"go.mod": mod, "main.go": main} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			return "", 0, err
		}
	}
	out := filepath.Join(dir, "probe")
	args := append([]string{"build", "-trimpath", "-ldflags=-s -w"}, buildFlags...)
	if _, err := ExecOut(dir, []string{"GOWORK=off", "GOFLAGS=-mod=mod"}, "go", append(args, "-o", out, ".")...); err != nil {
		return "", 0, err
	}
	st, err := os.Stat(out)
	if err != nil {
		return "", 0, err
	}
	return out, int(st.Size()), nil
}
