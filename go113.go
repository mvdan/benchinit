// Copyright (c) 2019, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

// +build go1.13

// Go 1.13 uses one symbols per package: an inittask, which contains information
// like the old initdone integer.

package main

const initCode = "inittask"
