# Testing

## Running

```sh
go test -race -cover .    # tests with race detector and coverage summary
go run ./scripts/check    # every gate, as CI and the factory run it
```

## Policy

- Statement coverage of the library (every package except `scripts/`) must stay at or above 80 %; the gate fails below it.
- Tests live in package `health_test` and exercise the public API only. They assert observable effects: return values, returned errors (`errors.Is`) and their messages.
- Tests use a fake clock through `Config.Now` and channels for coordination. The only real timer test is a check that ignores its context and blocks, with a 20 ms timeout, asserting that `Run` returns in under 2 s.
- Prefer table-driven tests with `t.Run` subtests. `example_test.go` holds `ExampleNew` for pkg.go.dev, verified through `// Output:` comments.
- The race detector runs whenever cgo and a C compiler are available (always in Linux CI); the gate prints why when it has to run without it.
- Each bug fix starts with a failing test that reproduces it.
- Tests are deterministic: no network, no wall clock, no dependence on test order.

## Layout

`<file>_test.go` sits next to the file it tests. Fixtures and golden files go in `testdata/`. `doc.go` describes liveness versus readiness and the error-exposure threat note.
