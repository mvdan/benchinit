// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import (
	"flag"
	"fmt"
	"os"

	"golang.org/x/tools/go/packages"
)

func main() {
	os.Exit(main1())
}

func main1() int {
	cfg := &packages.Config{Mode: packages.LoadImports}
	pkgs, err := packages.Load(cfg, flag.Args()...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		return 1
	}
	if packages.PrintErrors(pkgs) > 0 {
		return 1
	}

	// Print the names of the source files
	// for each package listed on the command line.
	for _, pkg := range pkgs {
		if err := benchmark(pkg); err != nil {
			fmt.Fprintf(os.Stderr, "benchmark: %v\n", err)
			return 1
		}
	}
	return 0
}

func benchmark(pkg *packages.Package) error {
	fmt.Println(pkg.PkgPath)
	return nil
}
