// Package healthaggregator runs dependency health checks concurrently, with a
// timeout per check, and aggregates them into a Report whose status is up,
// degraded or down.
//
// A failing critical check makes the report down; a failing non-critical check
// only makes it degraded. Reports can be cached for a TTL, concurrent callers
// share a single refresh, and Handler serves the report as JSON for
// Kubernetes probes and load balancers.
//
// # Liveness versus readiness
//
// Use two aggregators. A liveness aggregator should hold no external
// dependencies (or none at all), so a slow database never gets a pod killed.
// A readiness aggregator holds the dependencies, marking the ones the service
// cannot work without as Critical and the rest as optional, so they only
// degrade the answer. Mount them on different paths, for example /livez and
// /readyz.
//
// # Checks must honour their context
//
// Every check runs in its own goroutine under a context with a timeout. The
// aggregator stops waiting when the timeout expires and reports the check as
// down, but it cannot stop a function that ignores its context: that goroutine
// leaks until the function returns. To bound the damage, at most one invocation
// of each check is in flight; while it is still running the check is reported
// down with "previous run still in progress" and is not started again.
//
// # Threat note
//
// Health endpoints are often unauthenticated. Error text from dependencies can
// reveal hosts, ports or credentials, so it is hidden by default: with
// Config.ErrorDetails false every failure is reported as "failed". Enable
// ErrorDetails only on endpoints that are not reachable by untrusted callers.
// Error text is cut to 256 bytes on a rune boundary, names are limited to 64
// bytes, at most 1024 checks can be registered, and the handler never emits
// stack traces. A shared refresh is never cancelled by one caller: a caller
// that gives up receives a synthesized down report that is not cached.
package healthaggregator
