# Detached single-flight refresh with per-check goroutines

Status: accepted (generated, pending human review)

## Context

Health probes must answer fast and bounded even when a dependency hangs, must not let one impatient caller (for example a probe whose connection closed) cancel work that other callers wait for, and must not leak internal error text. Third-party single-flight helpers are not allowed.

## Decision

A mutex guards the cached Report and at most one in-flight refresh, represented by a done channel. The refresh runs in its own goroutine under context.WithoutCancel of the starting caller, so no caller's cancellation or deadline reaches shared work. Every caller selects on the done channel and its own ctx.Done(); a caller that gives up gets a synthesized, never-cached down report while the refresh continues and its result is cached for the others. Each check runs in its own goroutine under context.WithTimeout; a check that ignores its context is abandoned and an atomic flag prevents a second invocation until it returns, so leaks are bounded at one goroutine per check. Panics are recovered, error text is hidden unless ErrorDetails is set and is cut to 256 bytes on a rune boundary. Errors use a single *Error base type with stable codes; registrations are capped at 1024 checks.

## Consequences

Probes return within the largest timeout regardless of hung dependencies, and shared work is never poisoned by one caller. A check that never honours its context leaks one goroutine and is reported as still in progress until it returns. A Register during a refresh does not affect that refresh. OnCheck runs on the refresh goroutine after the result is published, so it may call Run but a slow callback does not delay callers.
