# benchinit

Benchmark the initialization cost of your packages or programs.

	go get -u mvdan.cc/benchinit

### Quickstart

Benchmarking a single package is simple:

	benchinit cmd/go

You can also include all dependencies in the benchmark:

	benchinit -r cmd/go

Finally, like any other benchmark, you can pass in `go test` flags:

	benchinit -r -count=5 -benchtime=2s cmd/go

### Further reading

This tool was result of a discussion with @josharian. You can read more about
Josh's idea in his blog post: http://commaok.xyz/post/benchmark-init/
