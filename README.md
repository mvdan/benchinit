# benchinit

Benchmark the initialization cost of your packages or programs.
Requires Go 1.18 or later.

	go install mvdan.cc/benchinit@latest

This includes the cost of `init` functions, plus initialising globals. In other
words, a package's contribution to the slowness before `main` is run.

### Quickstart

Benchmarking a single package is simple:

	benchinit cmd/go

You can benchmark multiple packages too; there must be at most one main package:

	benchinit cmd/go go/parser go/build

You can also include all dependencies in the benchmark:

	benchinit -r cmd/go

Finally, like any other benchmark, you can pass in `go test` flags:

	benchinit -r -count=5 -benchtime=2s cmd/go

### Further reading

The original tool was result of a discussion with [@josharian](https://github.com/josharian).
You can read more about Josh's idea in his [blog post](https://commaok.xyz/post/benchmark-init/).
Since then, `GODEBUG=inittrace=1` was [added in Go 1.16](https://go.dev/doc/go1.16#runtime),
which this tool now uses.
