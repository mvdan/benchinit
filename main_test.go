// Copyright (c) 2018, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import (
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/gotooltest"
	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"benchinit": main,
	})
}

func TestScripts(t *testing.T) {
	t.Parallel()
	params := testscript.Params{
		Dir:                 filepath.Join("testdata", "script"),
		RequireExplicitExec: true,
	}
	if err := gotooltest.Setup(&params); err != nil {
		t.Fatal(err)
	}
	testscript.Run(t, params)
}
