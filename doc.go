// Package healthaggregator is a small, dependency-free Go library: Aggregate dependency health checks concurrently with per-check timeouts, critical versus optional checks, a degraded state, caching and an HTTP handler
//
// The public API is every exported identifier of this root package. Functions are pure: they do
// no I/O, keep no global mutable state and report invalid input through returned errors.
package healthaggregator
