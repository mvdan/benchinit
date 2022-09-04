// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"golang.org/x/tools/go/packages"
)

func main() { os.Exit(main1()) }

func main1() int {
	// Figure out which flags should be passed on to 'go test'.
	testflags, rest := lazyFlagParse(os.Args[1:])
	flagSet.Usage = usage
	if err := flagSet.Parse(rest); err != nil {
		if err != flag.ErrHelp {
			fmt.Fprintf(os.Stderr, "flag: %v\n", err)
			usage()
		}
		return 2
	}

	// Load the packages.
	cfg := &packages.Config{Mode: packages.LoadAllSyntax}
	pkgs, err := packages.Load(cfg, flagSet.Args()...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		return 1
	}
	if packages.PrintErrors(pkgs) > 0 {
		return 1
	}

	// From this point onwards, errors are straightforward.
	if err := doBench(pkgs, testflags); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func doBench(pkgs []*packages.Package, testflags []string) error {
	// Prepare the packages to be benchmarked.
	tmpDir, err := os.MkdirTemp("", "benchinit")
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	overlay := struct{ Replace map[string]string }{
		Replace: make(map[string]string, len(pkgs)),
	}
	var mainPkg *packages.Package
	var mainDir string
	for _, pkg := range pkgs {
		if pkg.Name != "main" {
			continue
		}
		if mainPkg != nil {
			return fmt.Errorf("can only benchmark up to one main package at a time; found %s and %s", mainPkg.PkgPath, pkg.PkgPath)
		}
		mainPkg = pkg
		// Until go/packages.Package.Dir exists.
		mainDir = filepath.Dir(pkg.GoFiles[0])
	}
	if mainPkg == nil {
		// Until go/packages.Package.Dir exists.
		mainPkg = pkgs[0]
		mainDir = filepath.Dir(pkgs[0].GoFiles[0])
	}
	const genName = "benchinit_generated_test.go"
	replaceDst := filepath.Join(tmpDir, genName)

	tmpFile, err := os.Create(replaceDst)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	tmplData := struct {
		MainPkgName string
		Pkgs        []*packages.Package
	}{
		MainPkgName: mainPkg.Name,
		Pkgs:        pkgs,
	}
	// for debugging
	// println("--")
	// mainTmpl.Execute(os.Stderr, tmplData)
	// println("--")

	if err := mainTmpl.Execute(tmpFile, tmplData); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	args := []string{
		"test",
		"-run=^$",                              // disable all tests
		"-vet=off",                             // disable vet
		"-bench=^BenchmarkGeneratedBenchinit$", // only run the one benchmark
	}

	replaceSrc := filepath.Join(mainDir, genName)
	overlay.Replace[replaceSrc] = replaceDst
	overlayBytes, err := json.Marshal(overlay)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	overlayPath := filepath.Join(tmpDir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlayBytes, 0o666); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	args = append(args, "-overlay="+overlayPath)

	// Benchmark the packages with 'go test -bench'.
	args = append(args, testflags...) // add the user's test args
	args = append(args, mainDir)
	cmd := exec.Command("go", args...)
	pr, pw, err := os.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
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
	if waitErr != nil {
		// TODO: use ExitError.ExitCode() once we only support 1.12 and later.
		return fmt.Errorf("wait: %v; output:\n%s", waitErr, errorBuffer.Bytes())
	}
	if resultsPrinted == 0 {
		return fmt.Errorf("got no results; output:\n%s", errorBuffer.Bytes())
	}
	return nil
}

// TODO: the import mechanism means we don't support main packages right now.
// Importing the main package is only possible by placing the test file in it,
// meaning that we should only support one main package per invocation at most.

var mainTmpl = template.Must(template.New("").Parse(`
package {{ .MainPkgName }}_test

import (
	"os"
	"strconv"
	"os/exec"
	"testing"
	"time"
	"fmt"
	"strings"
	"regexp"

	{{ range $_, $pkg := .Pkgs -}}
	_ {{ printf "%q" $pkg.PkgPath }}
	{{- end }}
)

func BenchmarkGeneratedBenchinit(b *testing.B) {
	execPath, err := os.Executable()
	if err != nil {
		b.Fatal(err)
	}
	pkgs := []string{
		{{ range $_, $pkg := .Pkgs -}}
			{{ printf "%q" $pkg.PkgPath }},
		{{- end }}
	}
	type totals struct {
		Clock  time.Duration
		Bytes  uint64
		Allocs uint64
	}
	pkgTotals := make(map[string]*totals, len(pkgs))
	for _, pkg := range pkgs {
		pkgTotals[pkg] = new(totals)
	}

	rxInitTrace := regexp.MustCompile(` + "`" + `(?m)^init (?P<pkg>[^ ]+) (?P<time>@[^ ]+ [^ ]+), (?P<clock>[^ ]+ [^ ]+) clock, (?P<bytes>[^ ]+) bytes, (?P<allocs>[^ ]+) allocs$` + "`" + `)
	rxIndexPkg := rxInitTrace.SubexpIndex("pkg")
	rxIndexClock := rxInitTrace.SubexpIndex("clock")
	rxIndexBytes := rxInitTrace.SubexpIndex("bytes")
	rxIndexAllocs := rxInitTrace.SubexpIndex("allocs")

	for i := 0; i < b.N; i++ {
		cmd := exec.Command(execPath, "-h")
		// TODO: do not override existing GODEBUG values
		cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
		out, err := cmd.CombinedOutput()
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			// Sometimes -h will result in an exit code 2 rather than 0.
		} else if err != nil {
			b.Fatalf("%v: %s", err, out)
		}
		for _, match := range rxInitTrace.FindAllSubmatch(out, -1) {
			pkg := string(match[rxIndexPkg])
			totals := pkgTotals[pkg]
			if totals == nil {
				continue // we are not interested in this package
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
	for _, pkg := range pkgs {
		totals := *pkgTotals[pkg]

		// Turn "golang.org/x/foo" into "GolangOrgXFoo".
		name := pkg
		name = strings.ReplaceAll(name, "/", " ")
		name = strings.ReplaceAll(name, ".", " ")
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
`))

// testFlag is copied from cmd/go/internal/test/testflag.go's testFlagDefn, with
// small modifications.
var testFlagDefn = []struct {
	Name    string
	BoolVar bool
}{
	// local.
	{Name: "c", BoolVar: true},
	{Name: "i", BoolVar: true},
	{Name: "o"},
	{Name: "cover", BoolVar: true},
	{Name: "covermode"},
	{Name: "coverpkg"},
	{Name: "exec"},
	{Name: "json", BoolVar: true},
	{Name: "vet"},

	// Passed to 6.out, adding a "test." prefix to the name if necessary: -v becomes -test.v.
	{Name: "bench"},
	{Name: "benchmem", BoolVar: true},
	{Name: "benchtime"},
	{Name: "blockprofile"},
	{Name: "blockprofilerate"},
	{Name: "count"},
	{Name: "coverprofile"},
	{Name: "cpu"},
	{Name: "cpuprofile"},
	{Name: "failfast", BoolVar: true},
	{Name: "list"},
	{Name: "memprofile"},
	{Name: "memprofilerate"},
	{Name: "mutexprofile"},
	{Name: "mutexprofilefraction"},
	{Name: "outputdir"},
	{Name: "parallel"},
	{Name: "run"},
	{Name: "short", BoolVar: true},
	{Name: "timeout"},
	{Name: "trace"},
	{Name: "v", BoolVar: true},

	// extra build flags?
	{Name: "race", BoolVar: true},
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

// lazyFlagParse is similar to flag.Parse, but keeps 'go test' flags around so
// they can be passed on. We'll add our own benchinit flags at a later time.
func lazyFlagParse(args []string) (testflags, rest []string) {
_args:
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "" || arg == "--" || arg[0] != '-' {
			rest = append(rest, args[i:]...)
			break
		}
		for _, tflag := range testFlagDefn {
			if arg[1:] == tflag.Name {
				if tflag.BoolVar {
					// e.g. -benchmem
					testflags = append(testflags, arg)
					continue _args
				}
				next := ""
				if i+1 < len(args) {
					i++
					next = args[i]
				}
				testflags = append(testflags, arg, next)
				continue _args
			} else if strings.HasPrefix(arg[1:], tflag.Name+"=") {
				// e.g. -count=10
				testflags = append(testflags, arg)
				continue _args
			}
		}
		// Likely one of our flags. Leave it to flagSet.Parse.
		rest = append(rest, arg)
	}
	return testflags, rest
}
