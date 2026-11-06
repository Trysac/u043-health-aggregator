# Architecture

## Purpose

`github.com/Trysac/u043-health-aggregator` is a small, dependency-free Go library (package `healthaggregator`). It runs registered dependency checks concurrently, each with its own timeout, and aggregates the results into a Report with status up, degraded or down, cached for a configurable TTL and exposed through an HTTP handler.

## Shape

```text
caller code --> package healthaggregator (repository root)
                    |
                    +-- Aggregator: Register, Run, Handler
                    +-- single-flight refresh with a cached Report
                    +-- internal/ (optional private helpers)
```

- **Public API**: every exported identifier of the root package. Unexported identifiers and
  `internal/` packages may change without a major version.
- **Concurrency**: each check runs in its own goroutine with `context.WithTimeout` derived from a
  refresh context. One refresh runs at a time; concurrent callers wait for it. The refresh context
  is detached from every caller's cancellation, so a caller that gives up receives a synthesized
  down Report that is not cached, while the refresh continues for the others.
- **Caching**: a completed Report is reused while `Now()` minus its `CheckedAt` is below `CacheTTL`.
- **Errors**: invalid input returns an error. Exported sentinel errors let callers branch with
  `errors.Is`. Panics inside checks are recovered.
- **Side effects**: the library itself performs no I/O. Checks are caller code and perform whatever
  I/O they need. Any new I/O feature must accept an interface and be recorded in an ADR.

## Quality contract

`scripts/check` is the single definition of the gates; `fabrica.json` tells the factory and CI
how to run it. See [TESTING.md](TESTING.md).

## Decisions

Architecture decisions are recorded in [decisions/](decisions/) as numbered ADRs.
