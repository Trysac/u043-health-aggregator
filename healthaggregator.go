package healthaggregator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Status is the health state of one check or of a whole report.
type Status string

const (
	// StatusUp means everything that was checked is healthy.
	StatusUp Status = "up"

	// StatusDegraded means only non-critical checks failed.
	StatusDegraded Status = "degraded"

	// StatusDown means a critical check failed, or the caller stopped waiting.
	StatusDown Status = "down"
)

// MaxNameLength is the maximum length in bytes of a check name.
const MaxNameLength = 64

// MaxChecks is the maximum number of checks one Aggregator accepts.
const MaxChecks = 1024

// MaxErrorBytes is the maximum length in bytes of the error text of a check
// result when ErrorDetails is enabled.
const MaxErrorBytes = 256

const defaultTimeout = 2 * time.Second

const textFailed = "failed"

const textInProgress = "previous run still in progress"

var errInProgress = errors.New(textInProgress)

// Error is the base error type of this package. Code is stable and
// machine-readable; Message is short and never contains caller input.
type Error struct {
	// Code is a stable machine-readable identifier.
	Code string

	// Message is a short human-readable description.
	Message string
}

// Error implements the error interface.
func (e *Error) Error() string { return "healthaggregator: " + e.Message }

// ErrDuplicateCheck is returned by Register when the name is already registered.
var ErrDuplicateCheck = &Error{Code: "duplicate_check", Message: "a check with this name is already registered"}

// ErrInvalidName is returned by Register when the name is empty or longer than MaxNameLength bytes.
var ErrInvalidName = &Error{Code: "invalid_name", Message: "check name must be 1 to 64 bytes"}

// ErrNilFunc is returned by Register when the check has no Fn.
var ErrNilFunc = &Error{Code: "nil_func", Message: "check function must not be nil"}

// ErrTooManyChecks is returned by Register when MaxChecks checks are already registered.
var ErrTooManyChecks = &Error{Code: "too_many_checks", Message: "too many checks registered"}

// Check is one dependency probe.
//
// Fn must honour its context: a check that ignores it cannot be stopped and
// leaks its goroutine until it returns on its own.
type Check struct {
	// Name identifies the check; 1 to MaxNameLength bytes, unique per Aggregator.
	Name string

	// Fn runs the probe and returns nil when the dependency is healthy.
	Fn func(ctx context.Context) error

	// Timeout bounds one run; zero or negative uses Config.DefaultTimeout.
	Timeout time.Duration

	// Critical checks make the whole report down when they fail; failing
	// non-critical checks only degrade it.
	Critical bool
}

// Config configures an Aggregator. The zero value is usable.
type Config struct {
	// CacheTTL is how long a report is reused; zero disables caching.
	CacheTTL time.Duration

	// DefaultTimeout applies to checks without a Timeout; 2s when zero.
	DefaultTimeout time.Duration

	// ErrorDetails exposes error text (cut to MaxErrorBytes) in results.
	// When false every failure is reported as "failed", because internal
	// errors can reveal hosts or credentials.
	ErrorDetails bool

	// Now is the clock; time.Now when nil.
	Now func() time.Time

	// OnCheck, when set, is called once per executed check, in name order,
	// after the refresh result is published and outside every lock.
	OnCheck func(CheckResult)
}

// Report is the aggregated outcome of one refresh.
type Report struct {
	// Status is the aggregated status.
	Status Status `json:"status"`

	// CheckedAt is when the refresh finished, in UTC.
	CheckedAt time.Time `json:"checked_at"`

	// Checks holds one result per registered check, sorted by name.
	Checks []CheckResult `json:"checks"`
}

// CheckResult is the outcome of one check.
type CheckResult struct {
	// Name is the check name.
	Name string `json:"name"`

	// Status is StatusUp or StatusDown.
	Status Status `json:"status"`

	// DurationMS is the run time in milliseconds.
	DurationMS int64 `json:"duration_ms"`

	// Error is "failed" or, with ErrorDetails, the error text.
	Error string `json:"error,omitempty"`

	// Critical mirrors Check.Critical.
	Critical bool `json:"critical"`
}

type entry struct {
	check   Check
	running atomic.Bool
}

type flight struct {
	done   chan struct{}
	report Report
}

type outcome struct {
	index int
	err   error
	took  time.Duration
}

// Aggregator runs registered checks and aggregates them into a Report.
// It is safe for concurrent use.
type Aggregator struct {
	cfg Config

	mu       sync.Mutex
	entries  []*entry
	cached   *Report
	inflight *flight
	gen      uint64
}

// New creates an Aggregator, applying defaults to cfg.
func New(cfg Config) *Aggregator {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = defaultTimeout
	}
	if cfg.CacheTTL < 0 {
		cfg.CacheTTL = 0
	}
	return &Aggregator{cfg: cfg}
}

// Register adds a check and invalidates the cached report. It may be called
// after Run; a refresh already in flight keeps its original set of checks.
func (a *Aggregator) Register(c Check) error {
	if n := len(c.Name); n == 0 || n > MaxNameLength {
		return ErrInvalidName
	}
	if c.Fn == nil {
		return ErrNilFunc
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	idx := sort.Search(len(a.entries), func(i int) bool { return a.entries[i].check.Name >= c.Name })
	if idx < len(a.entries) && a.entries[idx].check.Name == c.Name {
		return ErrDuplicateCheck
	}
	if len(a.entries) >= MaxChecks {
		return ErrTooManyChecks
	}
	next := make([]*entry, 0, len(a.entries)+1)
	next = append(next, a.entries[:idx]...)
	next = append(next, &entry{check: c})
	next = append(next, a.entries[idx:]...)
	a.entries = next
	a.cached = nil
	a.gen++
	return nil
}

// Run returns the current Report, refreshing it when the cache is empty or
// expired. Concurrent callers share one refresh. The refresh is detached from
// the cancellation of every caller; a caller whose ctx ends first receives a
// synthesized, never cached, down report while the refresh carries on.
func (a *Aggregator) Run(ctx context.Context) Report {
	now := a.cfg.Now()
	a.mu.Lock()
	if c := a.cached; c != nil && a.cfg.CacheTTL > 0 {
		if age := now.Sub(c.CheckedAt); age >= 0 && age < a.cfg.CacheTTL {
			r := cloneReport(*c)
			a.mu.Unlock()
			return r
		}
	}
	entries := a.entries
	f := a.inflight
	if f == nil {
		f = &flight{done: make(chan struct{})}
		a.inflight = f
		go a.refresh(context.WithoutCancel(ctx), f, entries, a.gen)
	}
	a.mu.Unlock()

	select {
	case <-f.done:
		return cloneReport(f.report)
	case <-ctx.Done():
		select {
		case <-f.done:
			return cloneReport(f.report)
		default:
		}
		return a.abandoned(ctx.Err(), entries)
	}
}

func (a *Aggregator) abandoned(err error, entries []*entry) Report {
	text := a.errText(err)
	checks := make([]CheckResult, len(entries))
	for i, e := range entries {
		checks[i] = CheckResult{Name: e.check.Name, Status: StatusDown, Error: text, Critical: e.check.Critical}
	}
	return Report{Status: StatusDown, CheckedAt: a.cfg.Now().UTC(), Checks: checks}
}

func (a *Aggregator) refresh(ctx context.Context, f *flight, entries []*entry, gen uint64) {
	results, executed := a.execute(ctx, entries)
	rep := Report{Status: aggregate(results), CheckedAt: a.cfg.Now().UTC(), Checks: results}
	a.mu.Lock()
	f.report = rep
	if a.gen == gen && a.cfg.CacheTTL > 0 {
		c := cloneReport(rep)
		a.cached = &c
	}
	a.inflight = nil
	a.mu.Unlock()
	close(f.done)

	if a.cfg.OnCheck == nil {
		return
	}
	for i, r := range results {
		if executed[i] {
			a.notify(r)
		}
	}
}

func (a *Aggregator) notify(r CheckResult) {
	defer func() { _ = recover() }()
	a.cfg.OnCheck(r)
}

func (a *Aggregator) execute(ctx context.Context, entries []*entry) ([]CheckResult, []bool) {
	results := make([]CheckResult, len(entries))
	executed := make([]bool, len(entries))
	out := make(chan outcome, len(entries))
	pending := 0
	for i, e := range entries {
		results[i] = CheckResult{Name: e.check.Name, Critical: e.check.Critical}
		if !e.running.CompareAndSwap(false, true) {
			results[i].Status = StatusDown
			results[i].Error = a.errText(errInProgress)
			continue
		}
		executed[i] = true
		pending++
		go a.runOne(ctx, i, e, out)
	}
	for ; pending > 0; pending-- {
		o := <-out
		r := &results[o.index]
		r.DurationMS = o.took.Milliseconds()
		if o.err != nil {
			r.Status = StatusDown
			r.Error = a.errText(o.err)
		} else {
			r.Status = StatusUp
		}
	}
	return results, executed
}

func (a *Aggregator) runOne(ctx context.Context, index int, e *entry, out chan<- outcome) {
	start := a.cfg.Now()
	timeout := e.check.Timeout
	if timeout <= 0 {
		timeout = a.cfg.DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		err := call(cctx, e.check.Fn)
		e.running.Store(false)
		done <- err
	}()

	var err error
	select {
	case err = <-done:
	case <-cctx.Done():
		select {
		case err = <-done:
		default:
			err = cctx.Err()
		}
	}
	took := a.cfg.Now().Sub(start)
	if took < 0 {
		took = 0
	}
	out <- outcome{index: index, err: err, took: took}
}

func call(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("panic: %v", v)
		}
	}()
	return fn(ctx)
}

func (a *Aggregator) errText(err error) string {
	if !a.cfg.ErrorDetails {
		return textFailed
	}
	return truncate(err.Error())
}

func truncate(s string) string {
	if len(s) <= MaxErrorBytes {
		return s
	}
	cut := MaxErrorBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func aggregate(results []CheckResult) Status {
	status := StatusUp
	for _, r := range results {
		if r.Status != StatusDown {
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
	checks := make([]CheckResult, len(r.Checks))
	copy(checks, r.Checks)
	r.Checks = checks
	return r
}

// Handler returns an http.Handler serving the report as JSON. GET and HEAD
// answer 200 for up and degraded and 503 for down; other methods get 405.
// With ErrorDetails disabled only names, statuses and durations (plus the
// generic text "failed") are exposed.
func (a *Aggregator) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rep := a.Run(r.Context())
		h.Set("Content-Type", "application/json")
		code := http.StatusOK
		if rep.Status == StatusDown {
			code = http.StatusServiceUnavailable
		}
		w.WriteHeader(code)
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(rep)
	})
}
