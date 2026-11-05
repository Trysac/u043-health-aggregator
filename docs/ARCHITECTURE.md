# Architecture

## Purpose

`github.com/Trysac/u043-health-aggregator` is a small, dependency-free Go library. It runs dependency health checks concurrently, each with its own timeout, and aggregates them into a Report with status up, degraded or down. The package clause is `health`. It also provides an HTTP handler that serves the Report as JSON.

## Shape

```text
caller code --> package health (repository root)
                    |
                    +-- Aggregator: Register, Run, Handler
                    +-- cache + single-flight refresh (mutex and done channel)
                    +-- internal/ (optional private helpers)
```

- **Public API**: `Status` (`StatusUp`, `StatusDegraded`, `StatusDown`), `Check`, `Config`, `Report`, `CheckResult`, `New`, `(*Aggregator).Register`, `(*Aggregator).Run`, `(*Aggregator).Handler`, and the sentinel errors `ErrDuplicateCheck` and `ErrInvalidName`. Unexported identifiers and `internal/` packages may change without a major version.
- **Execution**: each check runs in its own goroutine under `context.WithTimeout` derived from the parent context. Results go to a channel buffered to the number of checks, so a check that never returns cannot block a send. Run waits on the results and on each check's `ctx.Done()`. A check whose context ends before it reports is recorded as failed with `ctx.Err()`. Panics are recovered inside the goroutine. A check with `Timeout` 0 uses `Config.DefaultTimeout` (2s when unset).
- **Status rules**: all checks pass gives up; only non-critical checks fail gives degraded; any critical failure gives down. Zero checks gives up. Results are sorted by name.
- **Caching**: with `CacheTTL` above 0, the Report and its timestamp are stored under a mutex. Concurrent callers during a refresh share one in-flight run. A refresh whose parent context ended early is returned to its callers but not stored. `Register` after `Run` is allowed and invalidates the cache. `Config.Now` supplies the clock for timestamps, durations and TTL.
- **Errors**: by default a failed check reports the error text `failed`. With `ErrorDetails` true, error text is cut to 256 bytes on a UTF-8 rune boundary. Invalid registrations return the sentinel errors, and the library never panics on input, prints or exits.
- **Handler**: GET and HEAD return 200 for up and degraded and 503 for down, with `Content-Type: application/json` and `Cache-Control: no-store`. HEAD sends the same headers with an empty body. Other methods get 405 with `Allow: GET, HEAD`. The body is `{status, checked_at, checks: [{name, status, duration_ms, error (omitted when empty), critical}]}`. With `ErrorDetails` false the handler exposes only names, statuses and durations.
- **Side effects**: none beyond running the caller's check functions. `OnCheck`, when set, is called once per executed check outside locks. If a feature needs I/O, accept an interface instead of opening resources, and record the choice in an ADR.

## Quality contract

`scripts/check` is the single definition of the gates; `fabrica.json` tells the factory and CI how to run it. See [TESTING.md](TESTING.md).

## Decisions

Architecture decisions are recorded in [decisions/](decisions/) as numbered ADRs.
