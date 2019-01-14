// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import (
	"flag"
	"fmt"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"golang.org/x/tools/go/packages"
)

var (
	recursive = flagSet.Bool("r", false, "include inits of transitive dependencies")
)

func init() {
	flagSet.Usage = usage
}

func main() {
	os.Exit(main1())
}

func main1() int {
	// Figure out which flags should be passed on to 'go test'.
	testflags, rest := lazyFlagParse(os.Args[1:])
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

	// Prepare the packages to be benchmarked.
	for _, pkg := range pkgs {
		cleanup, err := setup(pkg)
		defer cleanup()
		if err != nil {
			fmt.Fprintf(os.Stderr, "benchmark: %v\n", err)
			return 1
		}
	}

	// Benchmark the packages with 'go test -bench'.
	args := []string{"test",
		"-run=^$",                // disable all tests
		"-vet=off",               // disable vet
		"-bench=^BenchmarkInit$", // only run the one benchmark
	}
	args = append(args, testflags...) // add the user's test args
	for _, pkg := range pkgs {
		args = append(args, pkg.PkgPath)
	}
	cmd := exec.Command("go", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// TODO: use ExitError.ExitCode() once we only support 1.12 and
		// later.
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
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

const (
	benchFile = "benchinit_generated_test.go"
	stubFile  = "benchinit_generated_stub.go"
)

// setup prepares a package to be benchmarked. In particular, a number of
// temporary generated files are added to its directory on disk, to add the
// necessary BenchmarkInit function and all other necessary pieces.
func setup(pkg *packages.Package) (cleanup func(), _ error) {
	if len(pkg.GoFiles) == 0 {
		// No non-test Go files; no init work to benchmark. Do nothing,
		// and the 'go test -bench' command later will do little work
		// here.
		return func() {}, nil
	}

	var toDelete []string

	cleanup = func() {
		for _, path := range toDelete {
			if err := os.Remove(path); err != nil {
				// TODO: return the error instead? how likely is
				// it to happen?
				panic(err)
			}
		}
	}

	// Place the benchmark file in the same package, to ensure that we can
	// also benchmark transitive internal dependencies.
	// We assume 'go list' packages; all package files in the same directory.
	// TODO: since we use go/packages, add support for other build systems
	// and test it.
	dir := filepath.Dir(pkg.GoFiles[0])

	data := tmplData{
		Package: pkg,
	}
	roots := []*packages.Package{pkg}
	packages.Visit(roots, func(pkg *packages.Package) bool {
		if dontReinit[pkg.PkgPath] {
			return false // skip their imports as well.
		}
		data.Inits = append(data.Inits, pkg.PkgPath)
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			vr, ok := scope.Lookup(name).(*types.Var)
			if !ok {
				continue
			}
			t := vr.Type()
			for _, shouldZero := range globalsToZero {
				zeros := shouldZero(t)
				for _, zero := range zeros {
					zero.PkgPath = pkg.PkgPath
					zero.Name = vr.Name()
					zero.InitialSize = sizes.Sizeof(t)
					data.ToZero = append(data.ToZero, zero)
				}
			}
		}
		return *recursive // only benchmark deps when -r is given
	}, nil)

	bench := filepath.Join(dir, benchFile)
	if err := templateFile(bench, benchTmpl, data); err != nil {
		return cleanup, err
	}
	toDelete = append(toDelete, bench)

	stub := filepath.Join(dir, stubFile)
	if err := templateFile(stub, stubTmpl, data); err != nil {
		return cleanup, err
	}
	toDelete = append(toDelete, stub)
	return cleanup, nil
}

var dontReinit = map[string]bool{
	"runtime":   true, // messes up everything
	"testing":   true, // messes up the benchmark itself
	"os":        true, // messes up os.Stdout/Stderr via closing finalizers
	"os/signal": true, // messes up signal.Notify
	"time":      true, // messes up monotonic times
}

var sizes = types.SizesFor("gc", runtime.GOARCH)

// globalsToZero is a list of functions that look for subsets of bytes within
// globals that need zeroing between init calls.
var globalsToZero = [...]func(types.Type) []toZero{
	// Zero flag.FlagSet struct's "formal" field to avoid "flag redefined"
	// panics.
	// TODO: support many fields to zero.
	func(t types.Type) []toZero {
		t2, steps := lookupByType(t, "flag.FlagSet", 0)
		if t2 == nil {
			return nil
		}
		st := t2.Underlying().(*types.Struct)
		field, offs2 := fieldByName(st, "formal")
		steps = append(steps, zeroStep{Offset: offs2})
		steps = append(steps, zeroStep{ZeroSize: sizes.Sizeof(field.Type())})
		return []toZero{{
			Steps: steps,
		}}
	},
}

func lookupByType(t types.Type, tname string, level int) (types.Type, []zeroStep) {
	if t.String() == tname {
		return t, nil
	}
	if level++; level > 50 {
		return nil, nil // avoid loops in a simple way
	}
	var step zeroStep
	switch t := t.Underlying().(type) {
	case *types.Pointer:
		t2 := t.Elem()
		if t3, steps := lookupByType(t2, tname, level); t3 != nil {
			step.IndirectSize = sizes.Sizeof(t2)
			return t3, append([]zeroStep{step}, steps...)
		}
	case *types.Struct:
		var fields []*types.Var
		for i := 0; i < t.NumFields(); i++ {
			field := t.Field(i)
			fields = append(fields, field)
			t2 := field.Type()

			// TODO: quadratic work. perhaps replace with Alignof+Offsetof.
			step.Offset = sizes.Offsetsof(fields)[i]
			if t3, steps := lookupByType(t2, tname, level); t3 != nil {
				if step.Offset == 0 {
					return t3, steps
				}
				return t3, append([]zeroStep{step}, steps...)
			}
		}
	}
	return nil, nil
}

// fieldByName finds a struct's field by name, returning the field and its
// offset within the struct.
func fieldByName(st *types.Struct, name string) (vr *types.Var, offset int64) {
	var fields []*types.Var
	for i := 0; i < st.NumFields(); i++ {
		field := st.Field(i)
		fields = append(fields, field)
		if field.Name() == name {
			return field, sizes.Offsetsof(fields)[i]
		}
	}
	return nil, 0
}

// templateFile creates a file at path and fills its contents with the
// execution of the template with some data. It errors if the file exists or
// cannot be created.
func templateFile(path string, tmpl *template.Template, data interface{}) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := tmpl.Execute(f, data); err != nil {
		f.Close()
		return err
	}
	return f.Close() // check for an error
}

type tmplData struct {
	// Package is the non-test package being benchmarked.
	*packages.Package
	// Inits are the package paths of all the init functions to be
	// benchmarked. By default, this will only contain the import path of
	// the package itself. In recursive mode (-r), it will include the
	// import paths of transitive dependencies too, excluding the ones whose
	// initdone we can't mess with.
	Inits []string

	ToZero []toZero
}

type toZero struct {
	PkgPath, Name string

	InitialSize int64

	Steps []zeroStep
}

type zeroStep struct {
	// acts as a "oneof", so exactly one of the sections below must be set.

	ZeroSize int64 // zero a number of bytes

	IndirectSize int64 // pointer to a chunk of memory of a size

	Offset int64 // byte offset, after a possible indirect
}

// Don't use StartTimer and StopTimer, as they call ReadMemStats, which is way
// too expensive compared to most init functions.
// For example, on 'benchinit cmd/go', ReadMemStats was taking up over 90% of
// the cpu time, and throwing off all the numbers.

var benchTmpl = template.Must(template.New("").Parse(`
// Code generated by benchinit. DO NOT EDIT.

package {{.Name}}_test

import (
	"encoding/binary"
	"reflect"
	"testing"
	"unsafe" // must import unsafe to use go:linkname
)

func BenchmarkInit(b *testing.B) {
	// Allocs tend to matter too, and have no downsides.
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		deinit() // get ready to run init again
		_init()
	}
}

// avoid unused import errors
var _ binary.ByteOrder
var _ unsafe.Pointer
var _ reflect.SliceHeader

// deinit undoes the work that the init functions being benchmarked do. In
// particular, "initdone" is set to 0 to get init to do work again, and any
// globals known to cause issues are reset.
func deinit() {
	{{- range $i, $g := .ToZero }}
		{{- $last := (print "_zero" $i "_base") -}}
		{{- range $j, $s := .Steps }}
			{{- $cur := (print "_zero" $i "_" $j) -}}
			{{- if ne $s.ZeroSize 0 }}
	for i := 0; i < {{$s.ZeroSize}}; i++ {
		{{$last}}[i] = 0
	}

			{{- else if ne $s.IndirectSize 0 }}
	var {{$cur}} []byte
	if ptr := uintptr(binary.LittleEndian.Uint64({{$last}}[:])); ptr != 0 {
		// use a slice, to not copy the underlying bytes
		hdr := (*reflect.SliceHeader)(unsafe.Pointer(&{{$cur}}))
		hdr.Data = uintptr(binary.LittleEndian.Uint64({{$last}}[:]))
		hdr.Len = {{$s.IndirectSize}}
		hdr.Cap = {{$s.IndirectSize}}
	} else {
		dummy := [{{$s.IndirectSize}}]byte{}
		{{$cur}} = dummy[:]
	}

			{{- else }}{{/* $s.Offset */}}
	{{$cur}} := {{$last}}[{{$s.Offset}}:]

			{{- end -}}
			{{- $last = $cur -}}
		{{- end -}}
		{{- printf "\n" -}}
	{{- end }}

	{{- range $i, $_ := .Inits }}
	_initdone{{$i}} = 0
	{{- end }}
}

//go:linkname _init {{.PkgPath}}.init
func _init()

{{ range $i, $g := .ToZero }}
//go:linkname _zero{{$i}}_base {{$g.PkgPath}}.{{$g.Name}}
var _zero{{$i}}_base [{{$g.InitialSize}}]byte
{{- end }}

{{- range $i, $path := .Inits }}
//go:linkname _initdone{{$i}} {{$path}}.initdone·
var _initdone{{$i}} uint8
{{- end }}
`[1:]))

var stubTmpl = template.Must(template.New("").Parse(`
// Code generated by benchinit. DO NOT EDIT.

package {{.Name}}

func init() {}
`[1:]))
