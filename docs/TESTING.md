# Testing

## Running

```sh
go test -cover .          # tests with coverage summary
go run ./scripts/check    # every gate, as CI and the factory run it
```

## Policy

- Statement coverage of the library (every package except `scripts/`) must stay at or above 80 %;
  the gate fails below it.
- Tests live in package `healthaggregator_test` and exercise the public API only. They assert return
  values, returned errors (`errors.Is`) and their messages.
- Examples are runnable `Example` functions; `go test` verifies them through their `// Output:` comments.
- The race detector runs whenever cgo and a C compiler are available (always in Linux CI); the
  gate prints why when it has to run without it.
- Tests are deterministic: no network, no wall clock. Time is controlled through an injected `Now`
  function. The only real timer is the 20 ms timeout test, which asserts that the run returns in under 2 s.
- Each bug fix starts with a failing test that reproduces it.

## Coverage of behaviour

The tests are written against the acceptance criteria of the specification:

- status aggregation (up, degraded, down, zero checks), sorting and UTC timestamps;
- timeouts, the in-progress guard, panic recovery and error truncation;
- TTL caching with an injected clock;
- concurrent callers during one refresh, caller cancellation and re-entrant `OnCheck`;
- HTTP handler status codes, headers, HEAD and POST behaviour, and the exact JSON body;
- registration errors matched with `errors.Is`.

`example_test.go` contains `ExampleNew`, which is verified by `go test`.

## Layout

`<file>_test.go` sits next to the file it tests. Fixtures and golden files go in `testdata/`.
