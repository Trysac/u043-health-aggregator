// Package healthaggregator aggregates dependency health checks.
//
// An Aggregator runs registered checks concurrently, each with its own
// timeout, and combines the results into a Report whose status is up,
// degraded or down. A failing critical check makes the report down; a failing
// non-critical check only degrades it. Reports can be cached for a TTL and
// served over HTTP with Aggregator.Handler.
//
// # Liveness versus readiness
//
// Liveness answers "is the process alive" and should not depend on external
// systems; readiness answers "can the process serve traffic" and may check
// databases and caches. Use two aggregators: an empty one (or one with only
// in-process checks) for liveness, and one with dependency checks for
// readiness. Mount them on separate paths.
//
// # Execution model
//
// Every check runs in its own goroutine under a context with a deadline and
// reports on a buffered channel, so a check that never returns cannot block
// the run. Checks must honour their context: a check that ignores it is
// reported as failed when the deadline passes, but its goroutine stays alive
// until the function returns. Panics inside checks are recovered and reported
// as failures.
//
// # Threat note
//
// Error strings returned by checks can reveal host names, addresses or
// credentials. By default a failed check is reported only as "failed". With
// Config.ErrorDetails enabled, error text is cut to 256 bytes on a UTF-8
// boundary; enable it only on endpoints that are not publicly reachable.
// Check names are limited to 64 bytes, at most 256 checks can be registered
// and stack traces are never emitted.
package healthaggregator
