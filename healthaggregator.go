package healthaggregator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// MaxNameLength is the maximum length in bytes of a check name.
const MaxNameLength = 64

// MaxErrorLength is the maximum length in bytes of an error string exposed
// when Config.ErrorDetails is true.
const MaxErrorLength = 256

// MaxChecks is the maximum number of checks one Aggregator accepts.
const MaxChecks = 256

// DefaultCheckTimeout is the timeout used when neither the check nor the
// Config specifies one.
const DefaultCheckTimeout = 2 * time.Second

const failedMessage = "failed"

// Status is the health state of a check or of a whole report.
type Status string

// StatusUp means every check passed.
const StatusUp Status = "up"

// StatusDegraded means only non-critical checks failed.
const StatusDegraded Status = "degraded"

// StatusDown means at least one critical check failed.
const StatusDown Status = "down"

// Error is the base error type of this package. Every error returned by the
// package is an *Error with a stable, machine-readable Code.
type Error struct {
	// Code is a stable identifier such as "invalid_name".
	Code string

	// Message is a short human-readable description. It never contains input.
	Message string
}

// Error implements the error interface.
func (e *Error) Error() string {
	return "healthaggregator: " + e.Message
}

// ErrDuplicateCheck is returned by Register when a name is already in use.
var ErrDuplicateCheck = &Error{Code: "duplicate_check", Message: "a check with this name is already registered"}

// ErrInvalidName is returned by Register for an empty name, a name longer
// than MaxNameLength bytes or a name that is not valid UTF-8.
var ErrInvalidName = &Error{Code: "invalid_name", Message: "check name must be 1 to 64 bytes of valid UTF-8"}

// ErrInvalidCheck is returned by Register when the check function is nil.
var ErrInvalidCheck = &Error{Code: "invalid_check", Message: "check function must not be nil"}

// ErrTooManyChecks is returned by Register when MaxChecks is reached.
var ErrTooManyChecks = &Error{Code: "too_many_checks", Message: "too many checks registered"}

// Check is a single dependency probe.
type Check struct {
	// Name identifies the check in reports. It must be unique per Aggregator.
	Name string

	// Fn performs the probe and returns nil when the dependency is healthy.
	// It must honour ctx; a function that ignores ctx is reported as failed
	// when the timeout fires but its goroutine lives until it returns.
	Fn func(ctx context.Context) error

	// Timeout bounds the check. Zero or negative uses Config.DefaultTimeout.
	Timeout time.Duration

	// Critical marks a check whose failure makes the report StatusDown.
	// A failing non-critical check only degrades the report.
	Critical bool
}

// Config configures an Aggregator. The zero value is usable.
type Config struct {
	// CacheTTL is how long a report is reused. Zero disables caching.
	CacheTTL time.Duration

	// DefaultTimeout applies to checks without a Timeout. Zero means 2s.
	DefaultTimeout time.Duration

	// ErrorDetails exposes (truncated) error text in reports. It defaults to
	// false because error strings can reveal hosts or credentials.
	ErrorDetails bool

	// Now is the clock. It defaults to time.Now and must be safe for
	// concurrent use.
	Now func() time.Time

	// OnCheck, when set, is called once per executed check after the checks
	// finish, outside any lock. A panic in the callback is recovered.
	OnCheck func(CheckResult)
}

// Report is the aggregated outcome of one run.
type Report struct {
	// Status is the overall status.
	Status Status `json:"status"`

	// CheckedAt is when the run started.
	CheckedAt time.Time `json:"checked_at"`

	// Checks holds one result per check, sorted by name.
	Checks []CheckResult `json:"checks"`
}

// CheckResult is the outcome of one check.
type CheckResult struct {
	// Name is the check name.
	Name string `json:"name"`

	// Status is StatusUp or StatusDown.
	Status Status `json:"status"`

	// DurationMS is the elapsed time in milliseconds on the injected clock.
	DurationMS int64 `json:"duration_ms"`

	// Error is "failed" or, with ErrorDetails, the truncated error text.
	Error string `json:"error,omitempty"`

	// Critical mirrors Check.Critical.
	Critical bool `json:"critical"`
}

type flight struct {
	done   chan struct{}
	report Report
}

type indexed struct {
	i   int
	res CheckResult
}

// Aggregator runs registered checks and aggregates their results. It is safe
// for concurrent use.
type Aggregator struct {
	cfg Config

	mu       sync.Mutex
	checks   []Check
	names    map[string]struct{}
	gen      uint64
	cached   Report
	cachedAt time.Time
	hasCache bool
	inflight *flight
}

// New returns an Aggregator using cfg with defaults applied.
func New(cfg Config) *Aggregator {
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = DefaultCheckTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.CacheTTL < 0 {
		cfg.CacheTTL = 0
	}
	return &Aggregator{cfg: cfg, names: make(map[string]struct{})}
}

// Register adds a check. It may be called after Run; doing so invalidates the
// cached report. Errors are *Error values comparable with errors.Is.
func (a *Aggregator) Register(c Check) error {
	if c.Name == "" || len(c.Name) > MaxNameLength || !utf8.ValidString(c.Name) {
		return ErrInvalidName
	}
	if c.Fn == nil {
		return ErrInvalidCheck
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, dup := a.names[c.Name]; dup {
		return ErrDuplicateCheck
	}
	if len(a.checks) >= MaxChecks {
		return ErrTooManyChecks
	}
	a.checks = append(a.checks, c)
	a.names[c.Name] = struct{}{}
	a.gen++
	a.hasCache = false
	a.cached = Report{}
	return nil
}

// Run executes every check concurrently and returns the aggregated report.
//
// Fresh cached results are returned without running checks. Concurrent calls
// during a refresh share that single execution. A run whose ctx ended before
// completion is returned to its callers but never cached.
func (a *Aggregator) Run(ctx context.Context) Report {
	now := a.cfg.Now()
	a.mu.Lock()
	if a.cfg.CacheTTL > 0 && a.hasCache && now.Sub(a.cachedAt) < a.cfg.CacheTTL {
		r := cloneReport(a.cached)
		a.mu.Unlock()
		return r
	}
	if f := a.inflight; f != nil {
		a.mu.Unlock()
		<-f.done
		return cloneReport(f.report)
	}
	f := &flight{done: make(chan struct{})}
	a.inflight = f
	checks := append([]Check(nil), a.checks...)
	gen := a.gen
	a.mu.Unlock()

	report := a.execute(ctx, checks)

	a.mu.Lock()
	if ctx.Err() == nil && a.cfg.CacheTTL > 0 && a.gen == gen {
		a.cached = cloneReport(report)
		a.cachedAt = report.CheckedAt
		a.hasCache = true
	}
	f.report = report
	a.inflight = nil
	a.mu.Unlock()
	close(f.done)
	return cloneReport(report)
}

func (a *Aggregator) execute(ctx context.Context, checks []Check) Report {
	checkedAt := a.cfg.Now()
	out := make(chan indexed, len(checks))
	for i, c := range checks {
		go a.runCheck(ctx, i, c, out)
	}
	results := make([]CheckResult, len(checks))
	for range checks {
		r := <-out
		results[r.i] = r.res
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	status := aggregate(results)
	if a.cfg.OnCheck != nil {
		for _, r := range results {
			a.notify(r)
		}
	}
	return Report{Status: status, CheckedAt: checkedAt, Checks: results}
}

func (a *Aggregator) runCheck(parent context.Context, idx int, c Check, out chan<- indexed) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = a.cfg.DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	start := a.cfg.Now()
	err := runGuarded(ctx, c.Fn)
	ms := a.cfg.Now().Sub(start).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	res := CheckResult{Name: c.Name, Status: StatusUp, DurationMS: ms, Critical: c.Critical}
	if err != nil {
		res.Status = StatusDown
		res.Error = a.describe(err)
	}
	out <- indexed{i: idx, res: res}
}

// runGuarded runs fn in its own goroutine, recovers panics and returns as soon
// as fn finishes or ctx is done. A function ignoring ctx leaks only itself.
func runGuarded(ctx context.Context, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- fn(ctx)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		select {
		case err := <-done:
			return err
		default:
			return ctx.Err()
		}
	}
}

func (a *Aggregator) describe(err error) string {
	if !a.cfg.ErrorDetails {
		return failedMessage
	}
	msg := truncateUTF8(strings.ToValidUTF8(err.Error(), "\uFFFD"), MaxErrorLength)
	if msg == "" {
		return failedMessage
	}
	return msg
}

func (a *Aggregator) notify(r CheckResult) {
	defer func() { _ = recover() }()
	a.cfg.OnCheck(r)
}

// truncateUTF8 cuts s to at most limit bytes on a rune boundary.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func aggregate(results []CheckResult) Status {
	status := StatusUp
	for _, r := range results {
		if r.Status == StatusUp {
			continue
		}
		if r.Critical {
			return StatusDown
		}
		status = StatusDegraded
	}
	return status
}

func cloneReport(r Report) Report {
	r.Checks = append([]CheckResult{}, r.Checks...)
	return r
}

// Handler returns an http.Handler serving the report as JSON.
//
// GET and HEAD answer 200 for up and degraded and 503 for down, with
// Content-Type application/json and Cache-Control no-store. HEAD sends no
// body. Other methods get 405 with Allow: GET, HEAD. The request context is
// used for the run; stack traces are never included.
func (a *Aggregator) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		report := a.Run(r.Context())
		body, err := json.Marshal(report)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Length", strconv.Itoa(len(body)))
		code := http.StatusOK
		if report.Status == StatusDown {
			code = http.StatusServiceUnavailable
		}
		w.WriteHeader(code)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	})
}
