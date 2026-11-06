# health-aggregator

[![Go Reference](https://pkg.go.dev/badge/github.com/Trysac/u043-health-aggregator.svg)](https://pkg.go.dev/github.com/Trysac/u043-health-aggregator)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/Trysac/u043-health-aggregator/actions/workflows/ci.yml/badge.svg)](https://github.com/Trysac/u043-health-aggregator/actions/workflows/ci.yml)

Concurrent dependency health checks with per-check timeouts, critical versus optional checks, a degraded state, result caching and an HTTP handler.

## Problem

Services often expose a health endpoint that either always answers ok or fails the whole instance when a non-critical dependency, such as a cache, is slow. A hung check can also hang the probe itself. Kubernetes probes and load balancers need a fast, bounded answer that distinguishes down from degraded without leaking internal error text. This package runs checks concurrently with timeouts and aggregates them into up, degraded or down, with a JSON report.

## Install

```sh
go get github.com/Trysac/u043-health-aggregator
```

Requires Go 1.23 or newer. No dependencies outside the standard library.

## Usage

```go
package main

import (
	"context"
	"net/http"

	"github.com/Trysac/u043-health-aggregator"
)

func main() {
	agg := healthaggregator.New(healthaggregator.Config{})
	_ = agg.Register(healthaggregator.Check{Name: "db", Critical: true, Fn: func(ctx context.Context) error { return nil }})
	http.Handle("/readyz", agg.Handler())
	_ = http.ListenAndServe(":8080", nil)
}
```

## API

- `New(cfg Config) *Aggregator`. Config fields: `CacheTTL` (0 disables caching), `DefaultTimeout` (2s when 0), `ErrorDetails` (false by default), `Now` (defaults to `time.Now`), `OnCheck` (optional callback per executed check).
- `Check{Name, Fn, Timeout, Critical}`. `Fn func(ctx context.Context) error`. A `Timeout` of 0 uses `DefaultTimeout`.
- `(*Aggregator).Register(c Check) error`. Registering after `Run` is allowed and invalidates the cache.
- `(*Aggregator).Run(ctx context.Context) Report`. Status is `up` when all checks pass, `degraded` when only non-critical checks fail, and `down` when any critical check fails. Zero checks give `up`. Checks are sorted by name.
- `(*Aggregator).Handler() http.Handler`. GET returns 200 for `up` and `degraded`, and 503 for `down`. HEAD returns the same status and headers with an empty body. Other methods return 405 with `Allow: GET, HEAD`.
- Status constants: `StatusUp`, `StatusDegraded`, `StatusDown`.
- Errors, matched with `errors.Is`: `ErrDuplicateCheck`, `ErrInvalidName`, `ErrNilFunc`, `ErrTooManyChecks`.

Limits:

- Check names are 1 to 64 bytes.
- At most 1024 checks (`MaxChecks`).
- Error text is cut to at most 256 bytes on a UTF-8 rune boundary.
- A check that ignores its context is reported down at its timeout, but its goroutine keeps running until `Fn` returns. Checks must honour `ctx`.
- At most one invocation per check is in flight. While one is running, the check is reported down with `previous run still in progress`.
- A panic inside a check is recovered and reported as `panic: <value>`.

With `ErrorDetails` false, every failed check reports the error `failed`, and the handler exposes only names, statuses and durations.

See [docs/SECURITY.md](docs/SECURITY.md) for the threat model.

## Documentation

- [Architecture](docs/ARCHITECTURE.md) and [decisions](docs/decisions/)
- [Testing](docs/TESTING.md) and [security](docs/SECURITY.md)
- [Project record](docs/PROJECT.md) and [stack](docs/STACK.md)
- [Changelog](CHANGELOG.md) and [contributing guide](CONTRIBUTING.md)

## License

[MIT](LICENSE) (c) 2026 Miguel Gomez
