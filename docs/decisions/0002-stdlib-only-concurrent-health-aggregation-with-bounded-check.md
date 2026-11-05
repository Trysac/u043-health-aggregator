# Stdlib-only concurrent health aggregation with bounded checks and redacted errors

Status: accepted (generated, pending human review)

## Context

Services need a fast, bounded health answer that distinguishes down from degraded, cannot be hung by a slow dependency check, and does not leak internal error text. Existing libraries are unmaintained, lack a degraded state, or are larger than needed.

## Decision

Implement a small stdlib-only package. Each check runs in its own goroutine under context.WithTimeout and reports through a buffered channel, so a check that ignores its context leaks only its own goroutine and never blocks the run. Panics are recovered. Status is up, degraded (only non-critical failures) or down (a critical failure). A mutex-guarded TTL cache plus a done-channel single flight shares one refresh among concurrent callers; runs whose parent context ended or that predate a Register call are not cached. Errors are exposed as the fixed text 'failed' unless ErrorDetails is enabled, in which case they are cut to 256 bytes on a rune boundary. Names are limited to 64 bytes and checks to 256. All errors are *Error values with stable codes, compared via errors.Is.

## Consequences

Probes return within the configured timeouts even when a dependency hangs, and responses do not reveal hosts or credentials by default. Checks that ignore their context keep a goroutine alive until they return, which is documented. Callers joining a flight started with a cancelled context share its failed result. No external dependencies are added, and built-in database or HTTP checks, metrics export and the gRPC health protocol stay out of scope.
