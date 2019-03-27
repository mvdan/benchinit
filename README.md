# benchinit

Benchmark the initialization cost of your packages or programs.

	cd $(mktemp -d); go mod init tmp; go get mvdan.cc/benchinit

This includes the cost of `init` functions, plus initialising globals. In other
words, a package's contribution to the slowness before `main` is run.

Requires Go 1.12 or later.

### Quickstart

Benchmarking a single package is simple:

	benchinit cmd/go

You can also include all dependencies in the benchmark:

	benchinit -r cmd/go

Finally, like any other benchmark, you can pass in `go test` flags:

	benchinit -r -count=5 -benchtime=2s cmd/go

### Further reading

This tool was result of a discussion with [@josharian](https://github.com/josharian).
You can read more about Josh's idea in his [blog post](https://commaok.xyz/post/benchmark-init/).
