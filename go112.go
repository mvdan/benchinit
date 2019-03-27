// Copyright (c) 2019, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

// +build !go1.13

// Go 1.12 and earlier used two symbols per package: an init function, and an
// initdone integer.

package main

const initCode = "initdone"
