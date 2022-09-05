// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/exp/maps"
)

// TODO: "recursive" should exclude the init cost of "runtime" and its deps,
// as those can never be avoided as part of a program's init.
// And the same for "testing" and its deps, given that we run the benchmark as a test binary.
var recursive = flagSet.Bool("r", false, "include init costs of transitive dependencies")

// keep benchmain types in sync with benchmain_test.go.
type benchmainInput struct {
	AllImportPaths []string
	BenchPkgs      []benchmainPackage

	Recursive bool
}
type benchmainPackage struct {
	ImportPath string
	Deps       []string
}

//go:embed benchmain_test.go
var benchmainSource string

func main() { os.Exit(main1()) }

func main1() int {
	buildflags, testflags, rest := filterFlags(os.Args[1:])
	flagSet.Usage = usage
	if err := flagSet.Parse(rest); err != nil {
		if err == flag.ErrHelp {
			return 2
		}
	}
	pkgs, err := listPackages(flagSet.Args(), buildflags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// From this point onwards, errors are straightforward.
	if err := doBench(pkgs, buildflags, testflags); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func doBench(pkgs []*Package, buildflags, testflags []string) error {
	// Prepare the packages to be benchmarked.
	tmpDir, err := os.MkdirTemp("", "benchinit")
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	overlay := struct{ Replace map[string]string }{
		Replace: make(map[string]string, len(pkgs)),
	}
	input := benchmainInput{Recursive: *recursive}
	var mainPkg *Package
	allPkgs := make(map[string]bool)
	for _, pkg := range pkgs {
		allPkgs[pkg.ImportPath] = true
		for _, dep := range pkg.Deps {
			allPkgs[dep] = true
		}

		input.BenchPkgs = append(input.BenchPkgs, benchmainPackage{
			ImportPath: pkg.ImportPath,
			Deps:       pkg.Deps,
		})

		if pkg.Name != "main" {
			continue
		}
		if mainPkg != nil {
			return fmt.Errorf("can only benchmark up to one main package at a time; found %s and %s", mainPkg.ImportPath, pkg.ImportPath)
		}
		mainPkg = pkg
	}
	input.AllImportPaths = maps.Keys(allPkgs)
	sort.Strings(input.AllImportPaths)

	if mainPkg == nil {
		mainPkg = pkgs[0]
	}

	// Pretend like the main package we use for testing does not have any other
	// test files, as we are not interested in the init cost of tests.
	for _, testFile := range mainPkg.TestGoFiles {
		overlay.Replace[testFile] = ""
	}
	for _, testFile := range mainPkg.XTestGoFiles {
		overlay.Replace[testFile] = ""
	}

	// Place our template in the main package's directory via the overlay.
	const genName = "benchinit_generated_test.go"

	benchmain := benchmainSource
	benchmain = strings.Replace(benchmain, "package main_test\n", "package "+mainPkg.Name+"_test\n", 1)

	// for debugging
	// println("--")
	// println(benchmain)
	// println("--")
	replaceDst := filepath.Join(tmpDir, genName)
	if err := os.WriteFile(replaceDst, []byte(benchmain), 0o666); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	replaceSrc := filepath.Join(mainPkg.Dir, genName)
	overlay.Replace[replaceSrc] = replaceDst

	args := []string{
		"test",
		"-run=^$",                              // disable all tests
		"-vet=off",                             // disable vet
		"-bench=^BenchmarkGeneratedBenchinit$", // only run the one benchmark
	}

	overlayBytes, err := json.Marshal(overlay)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	overlayPath := filepath.Join(tmpDir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlayBytes, 0o666); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	args = append(args, "-overlay="+overlayPath)

	inputBytes, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	inputPath := filepath.Join(tmpDir, "input.json")
	if err := os.WriteFile(inputPath, inputBytes, 0o666); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	// Benchmark the packages with 'go test -bench'.
	args = append(args, buildflags...) // add the user's build flags
	args = append(args, testflags...)  // add the user's test flags
	args = append(args, mainPkg.Dir)
	cmd := exec.Command("go", args...)
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.Env = append(os.Environ(), "BENCHINIT_JSON_INPUT="+inputPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
		pw.Close()
	}()

	// Get our benchinit result lines.
	// Note that "go test" will often run a benchmark function multiple times
	// with increasing b.N values, to estimate an N for e.g. -benchtime=1s.
	// We only want the last benchinit result, the one directly followed by the
	// original continuation to the BenchmarkGeneratedBenchinit line. For example:
	//
	//	BenchmarkGeneratedBenchinit-16
	//	benchinit: BenchmarkGoBuild	1	7000 ns/op	5344 B/op	47 allocs/op
	//	continuation:
	//	benchinit: BenchmarkGoBuild	100	5880 ns/op	5080 B/op	45 allocs/op
	//	continuation:
	//	benchinit: BenchmarkGoBuild	1224	5803 ns/op	5059 B/op	45 allocs/op
	//	continuation: 1224	   961433 ns/op
	var errorBuffer bytes.Buffer // to print the whole output if we fail
	var benchinitResult string
	var resultsPrinted int
	rxBenchinitResult := regexp.MustCompile(`^benchinit: (.*)`)
	rxFinalResult := regexp.MustCompile(`^continuation:.*\d\s`)

	// These must be printed directly as-is during normal runs.
	// We don't do "FAIL", as we already print the entire output on any failure.
	// We don't do "ok" nor "pkg:", as we always only test one ad-hoc package.
	// Note that some may be "continuation" lines.
	rxPassthrough := regexp.MustCompile(`^(continuation: )?((goos:|goarch:|cpu:|PASS\s).*)`)

	scanner := bufio.NewScanner(io.TeeReader(pr, &errorBuffer))
	for scanner.Scan() {
		line := scanner.Text()
		if match := rxBenchinitResult.FindStringSubmatch(line); match != nil {
			benchinitResult = match[1]
		} else if rxFinalResult.MatchString(line) {
			if benchinitResult == "" {
				panic("did not find benchinit's result?")
			}
			fmt.Println(benchinitResult)
			resultsPrinted++
			benchinitResult = ""
		} else if match := rxPassthrough.FindStringSubmatch(line); match != nil {
			fmt.Println(match[2])
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner: %w", err)
	}
	if err := <-waitErr; err != nil {
		// TODO: use ExitError.ExitCode() once we only support 1.12 and later.
		return fmt.Errorf("wait: %v; output:\n%s", waitErr, errorBuffer.Bytes())
	}
	if resultsPrinted == 0 {
		return fmt.Errorf("got no results; output:\n%s", errorBuffer.Bytes())
	}
	return nil
}

var flagSet = flag.NewFlagSet("benchinit", flag.ContinueOnError)

func usage() {
	fmt.Fprintf(os.Stderr, `
Usage of benchinit:

	benchinit [benchinit flags] [go test flags] [packages]

For example:

	benchinit -count=10 .

All flags accepted by 'go test', including the benchmarking ones, should be
accepted. See 'go help testflag' for a complete list.
`[1:])
}
