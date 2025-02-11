# benchinit

Benchmark the initialization cost of your packages or programs.
Requires Go 1.23 or later.

	go install mvdan.cc/benchinit@latest

This includes the cost of `init` functions and initialising globals.
In other words, a package's contribution to the slowness before `main` is run.

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

The following design decisions were made:

* `GODEBUG=inittrace=1` requires us to run a new Go process for every benchmark
  iteration, so `benchinit` sets up a wrapping benchmark `BenchmarkInit` which
  does this and collects the `inittrace` output. `BenchmarkInit` then produces
  one `BenchmarkPkgPath` result per package passed to `benchinit`, which is
  shown to the user.

* `benchinit` supports most build and test flags, which are passed down as
  needed. For example, you can use `-benchtime` and `-count` to control how the
  benchmark is run, and `-tags` to use build tags. Note that some test flags
  like `-bench` aren't supported, as we always run only `BenchmarkInit`.

* To avoid building a new binary, `BenchmarkInit` reuses its own test binary to
  run the Go process for each benchmark iteration. To prevent test globals and
  `init` funcs from being part of the result, all `*_test.go` files are masked
  as deleted via `-overlay`. The same overlay is used to insert a temporary file
  containing `BenchmarkInit`.

* `BenchmarkInit` only runs one Go process per benchmark iteration, even when
  benchmarking multiple packages at once. This is possible since `inittrace`
  prints one line per package being initialized, so we only need to ensure
  the test binary imports all the necessary packages to initialize them.
  For the same reason, we can only benchmark one `main` package at a time.

* If none of the given packages are a `main` package, the benchmark is run from
  the first given package. This helps us support benchmarking internal packages.
