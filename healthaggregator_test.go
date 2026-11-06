package healthaggregator_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Trysac/u043-health-aggregator"
)

type check = healthaggregator.Check

type config = healthaggregator.Config

var epoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock { return &fakeClock{now: epoch} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func pass(context.Context) error { return nil }

func failWith(msg string) func(context.Context) error {
	return func(context.Context) error { return errors.New(msg) }
}

func newAgg(t *testing.T, cfg config, checks ...check) *healthaggregator.Aggregator {
	t.Helper()
	if cfg.Now == nil {
		cfg.Now = newClock().Now
	}
	a := healthaggregator.New(cfg)
	for _, c := range checks {
		if err := a.Register(c); err != nil {
			t.Fatalf("Register(%q) = %v", c.Name, err)
		}
	}
	return a
}

func blocker(t *testing.T, calls *atomic.Int32) func(context.Context) error {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return func(context.Context) error {
		calls.Add(1)
		<-release
		return nil
	}
}

func TestStatusAggregation(t *testing.T) {
	tests := []struct {
		name   string
		checks []check
		want   healthaggregator.Status
	}{
		{name: "all pass", checks: []check{{Name: "db", Fn: pass, Critical: true}, {Name: "cache", Fn: pass}}, want: healthaggregator.StatusUp},
		{name: "only optional fails", checks: []check{{Name: "db", Fn: pass, Critical: true}, {Name: "cache", Fn: failWith("x")}}, want: healthaggregator.StatusDegraded},
		{name: "critical fails", checks: []check{{Name: "db", Fn: failWith("x"), Critical: true}, {Name: "cache", Fn: pass}}, want: healthaggregator.StatusDown},
		{name: "critical and optional fail", checks: []check{{Name: "db", Fn: failWith("x"), Critical: true}, {Name: "cache", Fn: failWith("y")}}, want: healthaggregator.StatusDown},
		{name: "zero checks", checks: nil, want: healthaggregator.StatusUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAgg(t, config{}, tt.checks...)
			rep := a.Run(context.Background())
			if rep.Status != tt.want {
				t.Fatalf("Status = %q, want %q", rep.Status, tt.want)
			}
			if rep.Checks == nil || len(rep.Checks) != len(tt.checks) {
				t.Fatalf("Checks = %v, want %d entries and non-nil", rep.Checks, len(tt.checks))
			}
		})
	}
}

func TestChecksSortedAndUTC(t *testing.T) {
	zone := time.FixedZone("plus2", 2*60*60)
	now := func() time.Time { return epoch.In(zone) }
	a := newAgg(t, config{Now: now}, check{Name: "zeta", Fn: pass}, check{Name: "alpha", Fn: pass}, check{Name: "mid", Fn: pass})
	rep := a.Run(context.Background())
	names := make([]string, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "alpha,mid,zeta" {
		t.Fatalf("names = %q, want alpha,mid,zeta", got)
	}
	if rep.CheckedAt.Location() != time.UTC {
		t.Fatalf("CheckedAt location = %v, want UTC", rep.CheckedAt.Location())
	}
	if !rep.CheckedAt.Equal(epoch) {
		t.Fatalf("CheckedAt = %v, want %v", rep.CheckedAt, epoch)
	}
}

func TestTimeoutWithIgnoredContext(t *testing.T) {
	var calls atomic.Int32
	a := newAgg(t, config{ErrorDetails: true}, check{Name: "slow", Fn: blocker(t, &calls), Timeout: 20 * time.Millisecond, Critical: true})
	start := time.Now()
	rep := a.Run(context.Background())
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("Run took %v, want under 2s", elapsed)
	}
	if rep.Status != healthaggregator.StatusDown {
		t.Fatalf("Status = %q, want down", rep.Status)
	}
	got := rep.Checks[0]
	if got.Status != healthaggregator.StatusDown || got.Error != "context deadline exceeded" {
		t.Fatalf("result = %+v, want down with context deadline exceeded", got)
	}
	again := a.Run(context.Background())
	if n := calls.Load(); n != 1 {
		t.Fatalf("Fn called %d times, want 1", n)
	}
	if again.Checks[0].Error != "previous run still in progress" || again.Status != healthaggregator.StatusDown {
		t.Fatalf("second result = %+v (status %q), want previous run still in progress", again.Checks[0], again.Status)
	}
}

func TestFailuresHiddenByDefault(t *testing.T) {
	var calls atomic.Int32
	a := newAgg(t, config{}, check{Name: "a-fail", Fn: failWith("dial tcp 10.0.0.5:5432: refused")}, check{Name: "b-slow", Fn: blocker(t, &calls), Timeout: 20 * time.Millisecond})
	for run := 0; run < 2; run++ {
		rep := a.Run(context.Background())
		for _, c := range rep.Checks {
			if c.Status != healthaggregator.StatusDown || c.Error != "failed" {
				t.Fatalf("run %d: result = %+v, want down with Error failed", run, c)
			}
		}
	}
}

func TestErrorTruncation(t *testing.T) {
	tests := []struct {
		name    string
		message string
		wantLen int
	}{
		{name: "multi-byte text cut on a rune boundary", message: "a" + strings.Repeat("\u00e9", 200), wantLen: 255},
		{name: "ascii cut at the limit", message: strings.Repeat("x", 300), wantLen: 256},
		{name: "exactly at the limit is kept", message: strings.Repeat("x", 256), wantLen: 256},
		{name: "short message is kept", message: "boom", wantLen: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAgg(t, config{ErrorDetails: true}, check{Name: "c", Fn: failWith(tt.message)})
			got := a.Run(context.Background()).Checks[0].Error
			if len(got) != tt.wantLen {
				t.Fatalf("len(Error) = %d, want %d", len(got), tt.wantLen)
			}
			if !utf8.ValidString(got) || !strings.HasPrefix(tt.message, got) {
				t.Fatalf("Error %q is not a valid prefix of the message", got)
			}
		})
	}
}

func TestPanicIsRecovered(t *testing.T) {
	var calls atomic.Int32
	fn := func(context.Context) error {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		return nil
	}
	a := newAgg(t, config{ErrorDetails: true}, check{Name: "p", Fn: fn, Critical: true})
	rep := a.Run(context.Background())
	if rep.Status != healthaggregator.StatusDown || rep.Checks[0].Error != "panic: boom" {
		t.Fatalf("report = %+v, want down with panic: boom", rep)
	}
	if next := a.Run(context.Background()); next.Status != healthaggregator.StatusUp {
		t.Fatalf("next Status = %q, want up", next.Status)
	}

	hidden := newAgg(t, config{}, check{Name: "p", Fn: func(context.Context) error { panic("boom") }})
	if got := hidden.Run(context.Background()).Checks[0].Error; got != "failed" {
		t.Fatalf("hidden panic Error = %q, want failed", got)
	}
}

func TestCheckHonoursContext(t *testing.T) {
	fn := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	a := newAgg(t, config{ErrorDetails: true}, check{Name: "c", Fn: fn, Timeout: 20 * time.Millisecond})
	if got := a.Run(context.Background()).Checks[0].Error; got != "context deadline exceeded" {
		t.Fatalf("Error = %q, want context deadline exceeded", got)
	}
}

func TestDefaultTimeoutApplies(t *testing.T) {
	var calls atomic.Int32
	a := newAgg(t, config{DefaultTimeout: 20 * time.Millisecond, ErrorDetails: true}, check{Name: "c", Fn: blocker(t, &calls)})
	start := time.Now()
	rep := a.Run(context.Background())
	if time.Since(start) >= 2*time.Second || rep.Checks[0].Error != "context deadline exceeded" {
		t.Fatalf("result = %+v, want a quick deadline exceeded", rep.Checks[0])
	}
}

func TestCacheTTL(t *testing.T) {
	clk := newClock()
	var calls atomic.Int32
	fn := func(context.Context) error {
		calls.Add(1)
		return nil
	}
	a := newAgg(t, config{CacheTTL: 10 * time.Second, Now: clk.Now}, check{Name: "db", Fn: fn})
	a.Run(context.Background())
	a.Run(context.Background())
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
	clk.Advance(9 * time.Second)
	a.Run(context.Background())
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls after 9s = %d, want 1", n)
	}
	clk.Advance(time.Second)
	a.Run(context.Background())
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls after 10s = %d, want 2", n)
	}
	clk.Advance(-time.Minute)
	a.Run(context.Background())
	if n := calls.Load(); n != 3 {
		t.Fatalf("calls after the clock moved backwards = %d, want 3", n)
	}
}

func TestCacheDisabled(t *testing.T) {
	var calls atomic.Int32
	fn := func(context.Context) error {
		calls.Add(1)
		return nil
	}
	a := newAgg(t, config{CacheTTL: -time.Second}, check{Name: "db", Fn: fn})
	a.Run(context.Background())
	a.Run(context.Background())
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
}

func TestRegisterInvalidatesCache(t *testing.T) {
	var first, second atomic.Int32
	count := func(n *atomic.Int32) func(context.Context) error {
		return func(context.Context) error {
			n.Add(1)
			return nil
		}
	}
	a := newAgg(t, config{CacheTTL: 10 * time.Second}, check{Name: "a", Fn: count(&first)})
	a.Run(context.Background())
	if err := a.Register(check{Name: "b", Fn: count(&second)}); err != nil {
		t.Fatalf("Register = %v", err)
	}
	rep := a.Run(context.Background())
	if len(rep.Checks) != 2 || second.Load() != 1 || first.Load() != 2 {
		t.Fatalf("checks=%d first=%d second=%d, want 2, 2, 1", len(rep.Checks), first.Load(), second.Load())
	}
}

func TestReportIsolation(t *testing.T) {
	a := newAgg(t, config{CacheTTL: time.Hour}, check{Name: "db", Fn: pass})
	rep := a.Run(context.Background())
	rep.Checks[0].Name = "mutated"
	if got := a.Run(context.Background()).Checks[0].Name; got != "db" {
		t.Fatalf("cached Name = %q, want db", got)
	}
}

func TestConcurrentRunsShareRefreshAndSurviveCancel(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	fn := func(context.Context) error {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return nil
	}
	a := newAgg(t, config{CacheTTL: 10 * time.Second, ErrorDetails: true}, check{Name: "db", Fn: fn, Critical: true})

	ctxA, cancelA := context.WithCancel(context.Background())
	repA := make(chan healthaggregator.Report, 1)
	repB := make(chan healthaggregator.Report, 1)
	go func() { repA <- a.Run(ctxA) }()
	<-started
	go func() { repB <- a.Run(context.Background()) }()
	cancelA()

	select {
	case r := <-repA:
		if r.Status != healthaggregator.StatusDown || r.Checks[0].Error != "context canceled" || r.Checks[0].DurationMS != 0 || !r.Checks[0].Critical {
			t.Fatalf("caller A report = %+v, want a synthesized down report", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("caller A did not return promptly after cancellation")
	}

	close(release)
	select {
	case r := <-repB:
		if r.Status != healthaggregator.StatusUp {
			t.Fatalf("caller B Status = %q, want up", r.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("caller B did not receive the refresh")
	}

	if cached := a.Run(context.Background()); cached.Status != healthaggregator.StatusUp {
		t.Fatalf("cached Status = %q, want up", cached.Status)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("Fn called %d times, want 1", n)
	}
}

func TestAbandonedReportHidesErrorByDefault(t *testing.T) {
	var calls atomic.Int32
	a := newAgg(t, config{}, check{Name: "db", Fn: blocker(t, &calls)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep := a.Run(ctx)
	if rep.Status != healthaggregator.StatusDown || rep.Checks[0].Error != "failed" {
		t.Fatalf("report = %+v, want down with failed", rep)
	}
}

func TestOnCheckMayCallRun(t *testing.T) {
	var agg *healthaggregator.Aggregator
	var reentered atomic.Bool
	finished := make(chan struct{})
	cfg := config{Now: newClock().Now, OnCheck: func(healthaggregator.CheckResult) {
		if reentered.CompareAndSwap(false, true) {
			agg.Run(context.Background())
			close(finished)
		}
	}}
	agg = healthaggregator.New(cfg)
	if err := agg.Register(check{Name: "db", Fn: pass}); err != nil {
		t.Fatalf("Register = %v", err)
	}
	agg.Run(context.Background())
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("OnCheck calling Run deadlocked")
	}
}

func recv(t *testing.T, ch <-chan healthaggregator.CheckResult) healthaggregator.CheckResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("OnCheck was not called")
	}
	return healthaggregator.CheckResult{}
}

func TestOnCheckOrderAndSkips(t *testing.T) {
	seen := make(chan healthaggregator.CheckResult, 8)
	agg := newAgg(t, config{OnCheck: func(r healthaggregator.CheckResult) { seen <- r }}, check{Name: "b", Fn: pass}, check{Name: "a", Fn: failWith("x")})
	agg.Run(context.Background())
	first, second := recv(t, seen), recv(t, seen)
	if first.Name != "a" || first.Status != healthaggregator.StatusDown || second.Name != "b" {
		t.Fatalf("callbacks = %+v then %+v, want a (down) then b", first, second)
	}

	var calls atomic.Int32
	seenSlow := make(chan healthaggregator.CheckResult, 8)
	slow := newAgg(t, config{OnCheck: func(r healthaggregator.CheckResult) { seenSlow <- r }}, check{Name: "slow", Fn: blocker(t, &calls), Timeout: 20 * time.Millisecond})
	slow.Run(context.Background())
	recv(t, seenSlow)
	slow.Run(context.Background())
	if n := len(seenSlow); n != 0 {
		t.Fatalf("OnCheck called for a skipped check (%d extra calls)", n)
	}
}

func TestOnCheckPanicIsContained(t *testing.T) {
	called := make(chan struct{}, 4)
	agg := newAgg(t, config{OnCheck: func(healthaggregator.CheckResult) {
		called <- struct{}{}
		panic("callback")
	}}, check{Name: "db", Fn: pass})
	agg.Run(context.Background())
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("OnCheck was not called")
	}
	if rep := agg.Run(context.Background()); rep.Status != healthaggregator.StatusUp {
		t.Fatalf("Status = %q, want up", rep.Status)
	}
}

func TestDurationMS(t *testing.T) {
	clk := newClock()
	slowFn := func(context.Context) error {
		clk.Advance(150 * time.Millisecond)
		return nil
	}
	backFn := func(context.Context) error {
		clk.Advance(-5 * time.Second)
		return nil
	}
	a := newAgg(t, config{Now: clk.Now}, check{Name: "slow", Fn: slowFn})
	if got := a.Run(context.Background()).Checks[0].DurationMS; got != 150 {
		t.Fatalf("DurationMS = %d, want 150", got)
	}
	b := newAgg(t, config{Now: clk.Now}, check{Name: "back", Fn: backFn})
	if got := b.Run(context.Background()).Checks[0].DurationMS; got != 0 {
		t.Fatalf("DurationMS = %d, want 0 for a backwards clock", got)
	}
}

func TestNewDefaults(t *testing.T) {
	a := healthaggregator.New(healthaggregator.Config{})
	if err := a.Register(check{Name: "db", Fn: pass}); err != nil {
		t.Fatalf("Register = %v", err)
	}
	rep := a.Run(context.Background())
	if rep.Status != healthaggregator.StatusUp || rep.CheckedAt.IsZero() || rep.Checks[0].DurationMS < 0 {
		t.Fatalf("report = %+v, want up with a real timestamp", rep)
	}
}

func TestRegisterErrors(t *testing.T) {
	a := healthaggregator.New(config{})
	if err := a.Register(check{Name: "db", Fn: pass}); err != nil {
		t.Fatalf("Register(db) = %v", err)
	}
	tests := []struct {
		name string
		in   check
		want error
	}{
		{name: "duplicate", in: check{Name: "db", Fn: pass}, want: healthaggregator.ErrDuplicateCheck},
		{name: "empty name", in: check{Name: "", Fn: pass}, want: healthaggregator.ErrInvalidName},
		{name: "65 byte name", in: check{Name: strings.Repeat("n", 65), Fn: pass}, want: healthaggregator.ErrInvalidName},
		{name: "64 byte name", in: check{Name: strings.Repeat("n", 64), Fn: pass}, want: nil},
		{name: "nil function", in: check{Name: "nofn"}, want: healthaggregator.ErrNilFunc},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := a.Register(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("Register error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRegisterLimit(t *testing.T) {
	a := healthaggregator.New(config{})
	for i := 0; i < healthaggregator.MaxChecks; i++ {
		if err := a.Register(check{Name: "c" + strconv.Itoa(i), Fn: pass}); err != nil {
			t.Fatalf("Register #%d = %v", i, err)
		}
	}
	if err := a.Register(check{Name: "one-more", Fn: pass}); !errors.Is(err, healthaggregator.ErrTooManyChecks) {
		t.Fatalf("Register over the limit = %v, want ErrTooManyChecks", err)
	}
}

func TestErrorValues(t *testing.T) {
	err := healthaggregator.New(config{}).Register(check{Name: "", Fn: pass})
	var typed *healthaggregator.Error
	if !errors.As(err, &typed) || typed.Code != "invalid_name" {
		t.Fatalf("error = %v, want *Error with code invalid_name", err)
	}
	if !strings.HasPrefix(err.Error(), "healthaggregator: ") {
		t.Fatalf("Error() = %q, want the package prefix", err.Error())
	}
	codes := map[string]*healthaggregator.Error{"duplicate_check": healthaggregator.ErrDuplicateCheck, "nil_func": healthaggregator.ErrNilFunc, "too_many_checks": healthaggregator.ErrTooManyChecks}
	for code, e := range codes {
		if e.Code != code {
			t.Fatalf("Code = %q, want %q", e.Code, code)
		}
	}
}

func serve(a *healthaggregator.Aggregator, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/health", nil)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHandlerStatusCodesAndHeaders(t *testing.T) {
	tests := []struct {
		name   string
		checks []check
		code   int
	}{
		{name: "up", checks: []check{{Name: "db", Fn: pass, Critical: true}}, code: http.StatusOK},
		{name: "degraded", checks: []check{{Name: "cache", Fn: failWith("x")}}, code: http.StatusOK},
		{name: "down", checks: []check{{Name: "db", Fn: failWith("x"), Critical: true}}, code: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAgg(t, config{}, tt.checks...)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := serve(a, method)
				if rec.Code != tt.code {
					t.Fatalf("%s code = %d, want %d", method, rec.Code, tt.code)
				}
				wantHeaders := map[string]string{"Content-Type": "application/json", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"}
				for k, v := range wantHeaders {
					if got := rec.Header().Get(k); got != v {
						t.Fatalf("%s header %s = %q, want %q", method, k, got, v)
					}
				}
				if method == http.MethodHead && rec.Body.Len() != 0 {
					t.Fatalf("HEAD body = %q, want empty", rec.Body.String())
				}
				if method == http.MethodGet && rec.Body.Len() == 0 {
					t.Fatal("GET body is empty")
				}
			}
		})
	}
}

func TestHandlerMethodNotAllowed(t *testing.T) {
	a := newAgg(t, config{}, check{Name: "db", Fn: pass})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := serve(a, method)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("%s = %d Allow %q, want 405 GET, HEAD", method, rec.Code, rec.Header().Get("Allow"))
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("%s body = %q, want empty", method, rec.Body.String())
		}
	}
}

func TestHandlerExactBody(t *testing.T) {
	a := newAgg(t, config{Now: newClock().Now}, check{Name: "db", Fn: pass, Critical: true})
	want := `{"status":"up","checked_at":"2026-01-02T03:04:05Z","checks":[{"name":"db","status":"up","duration_ms":0,"critical":true}]}` + "\n"
	if got := serve(a, http.MethodGet).Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	empty := newAgg(t, config{Now: newClock().Now})
	wantEmpty := `{"status":"up","checked_at":"2026-01-02T03:04:05Z","checks":[]}` + "\n"
	if got := serve(empty, http.MethodGet).Body.String(); got != wantEmpty {
		t.Fatalf("empty body = %q, want %q", got, wantEmpty)
	}
}

func TestHandlerHidesInternalErrors(t *testing.T) {
	fn := failWith("dial tcp 10.0.0.5:5432: connection refused")
	hidden := newAgg(t, config{}, check{Name: "db", Fn: fn, Critical: true})
	body := serve(hidden, http.MethodGet).Body.String()
	if strings.Contains(body, "10.0.0.5") || strings.Contains(body, "refused") || !strings.Contains(body, `"error":"failed"`) {
		t.Fatalf("body leaks internal error text or lacks the generic one: %s", body)
	}

	shown := newAgg(t, config{ErrorDetails: true}, check{Name: "db", Fn: fn, Critical: true})
	if body := serve(shown, http.MethodGet).Body.String(); !strings.Contains(body, "10.0.0.5") {
		t.Fatalf("body with ErrorDetails lacks the error text: %s", body)
	}
}

func TestHandlerCanceledRequest(t *testing.T) {
	var calls atomic.Int32
	a := newAgg(t, config{}, check{Name: "db", Fn: blocker(t, &calls), Critical: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}
