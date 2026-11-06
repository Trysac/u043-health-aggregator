# Security notes

## Threat model

`github.com/Trysac/u043-health-aggregator` is a library without I/O of its own. Its checks are supplied by the caller, and its HTTP handler is mounted by the caller.

Defended:

- **Untrusted input to the library**: invalid names (empty or longer than 64 bytes), duplicate names, nil check functions and more than 1024 checks are rejected with explicit errors.
- **Error text exposure**: with `ErrorDetails` false (the default), every failed check reports only the fixed text `failed`, and the handler exposes only names, statuses and durations. Stack traces are never sent.
- **Oversized error text**: error text with details is cut to at most 256 bytes on a UTF-8 rune boundary.
- **Hung checks**: each check has its own timeout, so a blocked check cannot block the whole report. Its result is reported down at the timeout.
- **Panics in checks**: recovered and reported as `panic: <value>`; the aggregator keeps working.
- **Repeated invocation**: at most one invocation per check is in flight at a time.
- **Supply chain**: standard library only, `go.sum` verification, `govulncheck` in the local gates, Dependabot weekly updates, actions pinned by commit SHA, minimal workflow permissions.
- **Leaked secrets**: `gitleaks` runs in the local gates and in CI; `.env` files are git-ignored.

Not defended:

- **Error details when enabled**: with `ErrorDetails` true, the text returned by a check is exposed verbatim, up to the 256-byte cap. If an error contains hosts or credentials, they are exposed. Keep it false on publicly reachable endpoints.
- **Check code**: the library cannot sandbox `Fn`. A check that ignores its context keeps its goroutine running after its timeout, which leaks that goroutine until `Fn` returns.
- **Endpoint access control**: the handler has no authentication, authorization or rate limiting. Exposing it publicly reveals the names, statuses and durations of the registered checks.
- **Timing and availability of checks**: the report reflects the checks' behaviour within their timeouts. A slow dependency that stays under its timeout is reported as healthy.

## Publishing

Releases are git tags served through the Go module proxy and checksum database. No registry token
is stored in the repository.

To report a vulnerability, see [SECURITY.md](../SECURITY.md).
