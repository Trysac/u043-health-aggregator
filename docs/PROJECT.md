# Project record

## Objective

Provide a dependency-free Go package that runs service health checks concurrently with per-check timeouts and aggregates them into up, degraded or down with a JSON report. The package must give a fast, bounded answer for liveness and readiness probes without exposing internal error text by default.

## Scope

- In scope: the public API documented in [README.md](../README.md): `New`, `Register`, `Run`, `Handler`, critical and optional checks, the degraded state, TTL caching with single-flight refresh, and the optional `OnCheck` callback.
- Out of scope: built-in checks for specific systems, metrics, gRPC health checking, and anything that needs dependencies or I/O without an accepted ADR.

## Timeline

| Date | Event |
|---|---|
| 2026 | Repository created from the factory utility template. |

## Effort metrics

Measured values only; a value that has not been measured stays "pending measurement".

| Metric | Value |
|---|---|
| Tokens (input / output) | 283555 / 63730 |
| Hours (human) | pending measurement |
| Cost (USD) | 0.4307 |

## Factory run u043b (measured from API usage)

| Phase | Model | Batch | Input | Output | Cache write | Cache read | Cost (USD) |
|---|---|---|---|---|---|---|---|
| generation | claude-sonnet-5-5 | yes | 2551 | 49126 | 0 | 4285 | 0.2486 |
| fix-1 | claude-sonnet-5-5 | no | 8 | 1244 | 7720 | 55532 | 0.0373 |
| fix-2 | claude-sonnet-5-5 | no | 10 | 2796 | 7227 | 75580 | 0.0536 |
| fix-3 | claude-sonnet-5-5 | no | 10 | 1910 | 11569 | 80548 | 0.0561 |
| fix-4 | claude-sonnet-5-5 | no | 8 | 1201 | 5119 | 58079 | 0.0306 |
| docs | claude-haiku-5-5 | no | 6944 | 7453 | 0 | 0 | 0.0044 |

Total: $0.4307. Prices: Anthropic list prices.
