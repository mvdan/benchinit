// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// listPackages is akin to go/packages, but more specific to `go list`.
// In particular, it helps us reach fields like Dir and Deps.
// Moreover, by using `go test`, we are tightly coupled with cmd/go already.
func listPackages(args, flags []string) ([]*Package, error) {
	// Load the packages.
	var pkgs []*Package
	listArgs := []string{"list", "-json"}
	listArgs = append(listArgs, flags...)
	listArgs = append(listArgs, args...)
	cmd := exec.Command("go", listArgs...)
	pr, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	dec := json.NewDecoder(pr)
	for {
		var pkg Package
		if err := dec.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("list: %w", err)
		}
		pkgs = append(pkgs, &pkg)
	}
	if err := <-waitErr; err != nil {
		return nil, fmt.Errorf("list: %v:\n%s", err, stderr.Bytes())
	}
	return pkgs, nil
}

type Package struct {
	Dir        string
	ImportPath string
	Name       string

	GoFiles      []string
	TestGoFiles  []string
	XTestGoFiles []string
	Deps         []string
}

// The code below was initially borrowed from github.com/burrowers/garble,
// which has to pass flags down to cmd/go in a very similar way.

// forwardBuildFlags is obtained from 'go help build' as of Go 1.18beta1.
var forwardBuildFlags = map[string]bool{
	// These shouldn't be used in nested cmd/go calls.
	"a": false,
	"n": false,
	"x": false,
	"v": false,

	"asan":          true,
	"asmflags":      true,
	"buildmode":     true,
	"buildvcs":      true,
	"compiler":      true,
	"gccgoflags":    true,
	"gcflags":       true,
	"installsuffix": true,
	"ldflags":       true,
	"linkshared":    true,
	"mod":           true,
	"modcacherw":    true,
	"modfile":       true,
	"msan":          true,
	"overlay":       true,
	"p":             true,
	"pkgdir":        true,
	"race":          true,
	"tags":          true,
	"toolexec":      true,
	"trimpath":      true,
	"work":          true,
	"workfile":      true,
}

// booleanFlags is obtained from 'go help build' and 'go help testflag' as of Go 1.19beta1.
var booleanFlags = map[string]bool{
	// Global help.
	"h": true,

	// Shared build flags.
	"a":          true,
	"i":          true,
	"n":          true,
	"v":          true,
	"work":       true,
	"x":          true,
	"race":       true,
	"msan":       true,
	"asan":       true,
	"linkshared": true,
	"modcacherw": true,
	"trimpath":   true,
	"buildvcs":   true,

	// Test flags (TODO: support its special -args flag)
	"c":        true,
	"json":     true,
	"cover":    true,
	"failfast": true,
	"short":    true,
	"benchmem": true,

	// benchinit flags
	"r": true,
}

func filterFlags(flags []string) (build, test, rest []string) {
	for i := 0; i < len(flags); i++ {
		arg := flags[i]
		if !strings.HasPrefix(arg, "-") {
			return build, test, append(rest, flags[i:]...)
		}
		arg = strings.TrimLeft(arg, "-") // `-name` or `--name` to `name`

		name, _, _ := strings.Cut(arg, "=") // `name=value` to `name`

		start := i
		if strings.Contains(arg, "=") {
			// `-flag=value`
		} else if booleanFlags[name] {
			// `-boolflag`
		} else {
			// `-flag value`
			if i+1 < len(flags) {
				i++
			}
		}
		toAppend := flags[start : i+1]

		if forwardBuildFlags[name] { // build flag
			build = append(build, toAppend...)
		} else if name == "h" || flagSet.Lookup(name) != nil { // benchinit flag
			rest = append(rest, toAppend...)
		} else { // by elimination, a test flag
			test = append(test, toAppend...)
		}
	}
	return build, test, rest
}
