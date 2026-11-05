# health-aggregator

[![Go Reference](https://pkg.go.dev/badge/github.com/Trysac/u043-health-aggregator.svg)](https://pkg.go.dev/github.com/Trysac/u043-health-aggregator)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/Trysac/u043-health-aggregator/actions/workflows/ci.yml/badge.svg)](https://github.com/Trysac/u043-health-aggregator/actions/workflows/ci.yml)

Concurrent dependency health checks with per-check timeouts, critical versus optional checks, an up/degraded/down status, TTL caching and an HTTP handler. Stdlib only.

## Problem

Services often expose a health endpoint that either always reports ok, or fails the whole instance when a non-critical dependency such as a cache is slow. A hung check can also stall the probe itself. Kubernetes probes and load balancers need a fast, bounded answer that separates down from degraded without exposing internal error text, and teams currently hand-write sequential checks, timeouts and status logic for this.

## Install

```sh
go get github.com/Trysac/u043-health-aggregator
```

Requires Go 1.23 or newer. No dependencies outside the standard library.

## Usage

```go
agg := health.New(health.Config{CacheTTL: 5 * time.Second})
_ = agg.Register(health.Check{Name: "db", Critical: true, Fn: func(ctx context.Context) error { return nil }})
_ = agg.Register(health.Check{Name: "cache", Fn: func(ctx context.Context) error { return nil }})
report := agg.Run(context.Background())
fmt.Println(report.Status) // up
http.Handle("/readyz", agg.Handler())
log.Fatal(http.ListenAndServe(":8080", nil))
```

## Documentation

- [Architecture](docs/ARCHITECTURE.md) and [decisions](docs/decisions/)
- [Testing](docs/TESTING.md) and [security](docs/SECURITY.md)
- [Project record](docs/PROJECT.md) and [stack](docs/STACK.md)
- [Changelog](CHANGELOG.md) and [contributing guide](CONTRIBUTING.md)

## License

[MIT](LICENSE) (c) 2026 Miguel Gomez
