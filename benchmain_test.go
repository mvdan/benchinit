// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

//go:insert imports

// keep benchmain types in sync with main.go.
type benchmainInput struct {
	AllImportPaths []string
	BenchPkgs      []benchmainPackage

	Recursive bool
}
type benchmainPackage struct {
	ImportPath string
	Deps       []string
}

func BenchmarkGeneratedBenchinit(b *testing.B) {
	inputPath := os.Getenv("BENCHINIT_JSON_INPUT")
	if inputPath == "" {
		b.Fatal("this benchmark is only used internally by benchinit")
	}
	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		b.Fatal(err)
	}
	var input benchmainInput
	if err := json.Unmarshal(inputBytes, &input); err != nil {
		b.Fatal(err)
	}

	execPath, err := os.Executable()
	if err != nil {
		b.Fatal(err)
	}
	type totals struct {
		Clock  time.Duration
		Bytes  uint64
		Allocs uint64
	}
	pkgTotals := make(map[string]*totals, len(input.AllImportPaths))
	for _, pkg := range input.AllImportPaths {
		pkgTotals[pkg] = new(totals)
	}

	rxInitTrace := regexp.MustCompile(`(?m)^init (?P<pkg>[^ ]+) (?P<time>@[^ ]+ [^ ]+), (?P<clock>[^ ]+ [^ ]+) clock, (?P<bytes>[^ ]+) bytes, (?P<allocs>[^ ]+) allocs$`)
	rxIndexPkg := rxInitTrace.SubexpIndex("pkg")
	rxIndexClock := rxInitTrace.SubexpIndex("clock")
	rxIndexBytes := rxInitTrace.SubexpIndex("bytes")
	rxIndexAllocs := rxInitTrace.SubexpIndex("allocs")

	for i := 0; i < b.N; i++ {
		// We only want the test binary to run the init funcs,
		// so the help flag is one way to stop the main func early.
		// TODO: we could also use TestMain to stop right away if an env var is set.
		cmd := exec.Command(execPath, "-h")

		// Some Go code will behave slightly differently if it notices it's
		// running within a Go test, e.g. if os.Args[0] ends with ".test".
		// Make the execution look more like a regular program.
		cmd.Args[0] = "main"

		// TODO: do not override existing GODEBUG values
		cmd.Env = append(cmd.Environ(), "GODEBUG=inittrace=1")
		out, err := cmd.CombinedOutput()
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			// Sometimes -h will result in an exit code 2 rather than 0.
		} else if err != nil {
			b.Fatalf("%v: %s", err, out)
		}
		for _, match := range rxInitTrace.FindAllSubmatch(out, -1) {
			totals := pkgTotals[string(match[rxIndexPkg])]
			if totals == nil {
				continue // not a package we count, e.g. runtime
			}
			clock, err := time.ParseDuration(strings.Replace(string(match[rxIndexClock]), " ", "", 1))
			if err != nil {
				b.Fatal(err)
			}
			bytes, err := strconv.ParseUint(string(match[rxIndexBytes]), 10, 64)
			if err != nil {
				b.Fatal(err)
			}
			allocs, err := strconv.ParseUint(string(match[rxIndexAllocs]), 10, 64)
			if err != nil {
				b.Fatal(err)
			}
			totals.Clock += clock
			totals.Bytes += bytes
			totals.Allocs += allocs
		}
	}
	for _, pkg := range input.BenchPkgs {
		totals := *pkgTotals[pkg.ImportPath]

		if input.Recursive {
			for _, dep := range pkg.Deps {
				depTotals := *pkgTotals[dep]
				totals.Clock += depTotals.Clock
				totals.Bytes += depTotals.Bytes
				totals.Allocs += depTotals.Allocs
			}
		}

		// Turn "golang.org/x/foo" into "GolangOrgXFoo",
		// and "foo.bar/~user/go-baz" into "FooBarUserGoBaz".
		name := pkg.ImportPath
		name = strings.ReplaceAll(name, "/", " ")
		name = strings.ReplaceAll(name, ".", " ")
		name = strings.ReplaceAll(name, "~", " ")
		name = strings.ReplaceAll(name, "-", " ")
		name = strings.ReplaceAll(name, "_", " ")
		name = strings.Title(name)
		name = strings.ReplaceAll(name, " ", "")

		// We are printing between "BenchmarkGeneratedBenchinit" and its results,
		// which would usually go on the same line.
		// Break the line with a leading newline, show our separate results,
		// and then let the continuation of the original line go below.
		// TODO: include the -N CPU suffix, like in BenchmarkGeneratedBenchinit-16.
		fmt.Printf("\nbenchinit: Benchmark%s\t%d\t%d ns/op\t%d B/op\t%d allocs/op\ncontinuation: ",
			name, b.N, totals.Clock.Nanoseconds()/int64(b.N), totals.Bytes/uint64(b.N), totals.Allocs/uint64(b.N))
	}
	// TODO: complain if any of our packages are not seen N times
}
