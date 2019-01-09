// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"golang.org/x/tools/go/packages"
)

func main() {
	os.Exit(main1())
}

func main1() int {
	testflags, rest := lazyFlagParse(os.Args[1:]) // will exit on error

	cfg := &packages.Config{Mode: packages.LoadImports}
	pkgs, err := packages.Load(cfg, rest...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		return 1
	}
	if packages.PrintErrors(pkgs) > 0 {
		return 1
	}
	fmt.Println(pkgs)

	for _, pkg := range pkgs {
		status := "ok"
		start := time.Now()
		err := benchmark(pkg, testflags)
		if err != nil {
			fmt.Fprintf(os.Stderr, "benchmark: %v\n", err)
			status = "FAIL"
		}
		fmt.Printf("%s\t%s\t%s\n", status, pkg.PkgPath, time.Since(start))
	}
	return 0
}

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
				testflags = append(testflags, arg)
				if !tflag.BoolVar && i+1 < len(args) {
					// e.g. -count 10
					i++
					testflags = append(testflags, args[i])
				}
				continue _args
			} else if strings.HasPrefix(arg[1:], tflag.Name+"=") {
				// e.g. -count=10
				testflags = append(testflags, arg)
				continue _args
			}
		}
		fmt.Fprintf(os.Stderr, "flag provided but not defined: %s\n", arg)
		flag.Usage() // exits by default
	}
	return testflags, rest
}

func benchmark(pkg *packages.Package, testflags []string) error {
	// Place the benchmark file in the same package, to ensure that we can
	// also benchmark transitive internal dependencies.
	// We assume 'go list' packages; all package files in the same directory.
	// TODO: since we use go/packages, add support for other build systems
	// and test it.
	pkgDir := filepath.Dir(pkg.GoFiles[0])
	temp := filepath.Join(pkgDir, "benchinit_generated_test.go")
	if _, err := os.Lstat(temp); !os.IsNotExist(err) {
		return fmt.Errorf("temporary file %q already exists", temp)
	}
	f, err := os.Create(temp)
	if err != nil {
		return err
	}
	defer os.Remove(temp)

	if err := benchTmpl.Execute(f, pkg); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	flags := []string{"test",
		"-run=^$",                // disable all tests
		"-bench=^BenchmarkInit$", // only run the one benchmark
	}
	flags = append(flags, testflags...) // add the user's test flags
	flags = append(flags, ".")          // be explicit about the package

	cmd := exec.Command("go", flags...)
	cmd.Dir = pkgDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

var benchTmpl = template.Must(template.New("").Parse(`
package {{.Name}}_test

import (
        _ "{{.PkgPath}}" // must import a package to go:linkname into it
        "testing"
        _ "unsafe" // must import unsafe to use go:linkname
)

//go:linkname _initdone {{.PkgPath}}.initdone·
var _initdone uint8

//go:linkname _init {{.PkgPath}}.init
func _init()

func BenchmarkInit(b *testing.B) {
        for i := 0; i < b.N; i++ {
                _initdone = 0
                _init()
        }
}
`))
