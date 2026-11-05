package healthaggregator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	healthaggregator "github.com/Trysac/u043-health-aggregator"
)

type check = healthaggregator.Check

type config = healthaggregator.Config

var bg = context.Background()

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)}
}

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

func fail(context.Context) error { return errors.New("boom") }

func counting(n *int32, err error) func(context.Context) error {
	return func(context.Context) error {
		atomic.AddInt32(n, 1)
		return err
	}
}

func register(t *testing.T, a *healthaggregator.Aggregator, checks ...check) {
	t.Helper()
	for _, c := range checks {
		if err := a.Register(c); err != nil {
			t.Fatalf("Register(%q) = %v", c.Name, err)
		}
	}
}

func serve(a *healthaggregator.Aggregator, method string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(method, "/health", nil))
	return rec
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestRunStatus(t *testing.T) {
	tests := []struct {
		name   string
		checks []check
		want   healthaggregator.Status
	}{
		{name: "no checks", want: healthaggregator.StatusUp},
		{name: "all pass", checks: []check{{Name: "a", Fn: pass, Critical: true}, {Name: "b", Fn: pass}}, want: healthaggregator.StatusUp},
		{name: "optional fails", checks: []check{{Name: "db", Fn: pass, Critical: true}, {Name: "cache", Fn: fail}}, want: healthaggregator.StatusDegraded},
		{name: "critical fails", checks: []check{{Name: "db", Fn: fail, Critical: true}, {Name: "cache", Fn: pass}}, want: healthaggregator.StatusDown},
		{name: "both fail", checks: []check{{Name: "db", Fn: fail, Critical: true}, {Name: "cache", Fn: fail}}, want: healthaggregator.StatusDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := healthaggregator.New(config{})
			register(t, a, tt.checks...)
			got := a.Run(bg)
			if got.Status != tt.want {
				t.Fatalf("Status = %q, want %q", got.Status, tt.want)
			}
			if got.Checks == nil {
				t.Fatal("Checks must not be nil")
			}
			if len(got.Checks) != len(tt.checks) {
				t.Fatalf("len(Checks) = %d, want %d", len(got.Checks), len(tt.checks))
			}
		})
	}
}

func TestRunSortsByName(t *testing.T) {
	a := healthaggregator.New(config{})
	for _, name := range []string{"zeta", "alpha", "émile", "Beta"} {
		register(t, a, check{Name: name, Fn: pass})
	}
	var got []string
	for _, c := range a.Run(bg).Checks {
		got = append(got, c.Name)
	}
	want := []string{"Beta", "alpha", "zeta", "émile"}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestHungCheckIsReportedFailed(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	hung := func(context.Context) error {
		<-release
		return nil
	}
	a := healthaggregator.New(config{ErrorDetails: true})
	register(t, a, check{Name: "hung", Fn: hung, Timeout: 20 * time.Millisecond, Critical: true})
	begin := time.Now()
	got := a.Run(bg)
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("Run took %v", elapsed)
	}
	if got.Status != healthaggregator.StatusDown {
		t.Fatalf("Status = %q, want down", got.Status)
	}
	if got.Checks[0].Error != "context deadline exceeded" {
		t.Fatalf("Error = %q", got.Checks[0].Error)
	}
}

func TestErrorDetails(t *testing.T) {
	long := "x" + strings.Repeat("é", 200)
	tests := []struct {
		name    string
		details bool
		err     error
		want    string
	}{
		{name: "hidden by default", details: false, err: errors.New("boom"), want: "failed"},
		{name: "shown", details: true, err: errors.New("boom"), want: "boom"},
		{name: "empty message", details: true, err: errors.New(""), want: "failed"},
		{name: "invalid utf8", details: true, err: errors.New("a\xffb"), want: "a\uFFFDb"},
		{name: "multi-byte truncation", details: true, err: errors.New(long), want: "x" + strings.Repeat("é", 127)},
		{name: "ascii truncation", details: true, err: errors.New(strings.Repeat("a", 300)), want: strings.Repeat("a", 256)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := healthaggregator.New(config{ErrorDetails: tt.details})
			fn := func(context.Context) error { return tt.err }
			register(t, a, check{Name: "c", Fn: fn})
			got := a.Run(bg).Checks[0].Error
			if got != tt.want {
				t.Fatalf("Error = %q, want %q", got, tt.want)
			}
			if !utf8.ValidString(got) || len(got) > healthaggregator.MaxErrorLength {
				t.Fatalf("Error is invalid or too long: %d bytes", len(got))
			}
		})
	}
}

func TestPanicIsRecovered(t *testing.T) {
	var calls int32
	fn := func(context.Context) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			panic("kaboom")
		}
		return nil
	}
	a := healthaggregator.New(config{ErrorDetails: true})
	register(t, a, check{Name: "p", Fn: fn, Critical: true})
	first := a.Run(bg)
	if first.Status != healthaggregator.StatusDown || first.Checks[0].Error != "panic: kaboom" {
		t.Fatalf("first = %+v", first)
	}
	if second := a.Run(bg); second.Status != healthaggregator.StatusUp {
		t.Fatalf("second = %+v", second)
	}

	hidden := healthaggregator.New(config{})
	panicking := func(context.Context) error { panic("secret") }
	register(t, hidden, check{Name: "p", Fn: panicking})
	if got := hidden.Run(bg).Checks[0].Error; got != "failed" {
		t.Fatalf("Error = %q, want failed", got)
	}
}

func TestCacheReusesResultsWithinTTL(t *testing.T) {
	clk := newClock()
	var calls int32
	a := healthaggregator.New(config{CacheTTL: 10 * time.Second, Now: clk.Now})
	register(t, a, check{Name: "db", Fn: counting(&calls, nil)})
	first := a.Run(bg)
	first.Checks[0].Name = "mutated"
	second := a.Run(bg)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	if second.Checks[0].Name != "db" {
		t.Fatalf("cached report was mutated: %q", second.Checks[0].Name)
	}
	clk.Advance(11 * time.Second)
	a.Run(bg)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestCancelledRunIsNotCached(t *testing.T) {
	clk := newClock()
	var calls int32
	a := healthaggregator.New(config{CacheTTL: time.Hour, Now: clk.Now})
	register(t, a, check{Name: "db", Fn: counting(&calls, nil)})
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if got := a.Run(ctx); got.Status != healthaggregator.StatusUp && got.Checks[0].Status != healthaggregator.StatusDown {
		t.Fatalf("unexpected report %+v", got)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("calls = %d, want 0", got)
	}
	if got := a.Run(bg); got.Status != healthaggregator.StatusUp {
		t.Fatalf("Status = %q, want up", got.Status)
	}
	a.Run(bg)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

func TestRegisterAfterRunInvalidatesCache(t *testing.T) {
	clk := newClock()
	var calls int32
	a := healthaggregator.New(config{CacheTTL: time.Hour, Now: clk.Now})
	register(t, a, check{Name: "a", Fn: counting(&calls, nil)})
	a.Run(bg)
	register(t, a, check{Name: "b", Fn: pass})
	got := a.Run(bg)
	if len(got.Checks) != 2 {
		t.Fatalf("len(Checks) = %d, want 2", len(got.Checks))
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
}

func TestConcurrentRunsShareOneExecution(t *testing.T) {
	clk := newClock()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls int32
	slow := func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return nil
	}
	a := healthaggregator.New(config{CacheTTL: time.Hour, Now: clk.Now})
	register(t, a, check{Name: "slow", Fn: slow})
	const callers = 8
	reports := make([]healthaggregator.Report, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reports[i] = a.Run(bg)
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	for i, r := range reports {
		if r.Status != healthaggregator.StatusUp {
			t.Fatalf("report %d status = %q", i, r.Status)
		}
	}
}

func TestCancelledParentFailsFast(t *testing.T) {
	var calls int32
	a := healthaggregator.New(config{ErrorDetails: true})
	register(t, a, check{Name: "db", Fn: counting(&calls, nil), Critical: true}, check{Name: "cache", Fn: counting(&calls, nil)})
	ctx, cancel := context.WithCancel(bg)
	cancel()
	got := a.Run(ctx)
	if got.Status != healthaggregator.StatusDown {
		t.Fatalf("Status = %q, want down", got.Status)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("calls = %d, want 0", n)
	}
	for _, c := range got.Checks {
		if c.Status != healthaggregator.StatusDown || c.Error != "context canceled" {
			t.Fatalf("result = %+v", c)
		}
	}
}

func TestTimeoutSelection(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config
		timeout time.Duration
		min     time.Duration
		max     time.Duration
	}{
		{name: "check timeout", cfg: config{}, timeout: 5 * time.Second, min: 3 * time.Second, max: 5 * time.Second},
		{name: "config default", cfg: config{DefaultTimeout: 30 * time.Second}, min: 20 * time.Second, max: 30 * time.Second},
		{name: "builtin default", cfg: config{}, min: time.Second, max: 2 * time.Second},
		{name: "negative uses default", cfg: config{}, timeout: -1, min: time.Second, max: 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var remaining time.Duration
			probe := func(ctx context.Context) error {
				deadline, found := ctx.Deadline()
				if !found {
					return errors.New("no deadline")
				}
				remaining = time.Until(deadline)
				return nil
			}
			a := healthaggregator.New(tt.cfg)
			register(t, a, check{Name: "probe", Fn: probe, Timeout: tt.timeout})
			if got := a.Run(bg); got.Status != healthaggregator.StatusUp {
				t.Fatalf("Status = %q", got.Status)
			}
			if remaining <= tt.min || remaining > tt.max {
				t.Fatalf("remaining = %v, want in (%v, %v]", remaining, tt.min, tt.max)
			}
		})
	}
}

func TestDurationAndCheckedAtUseInjectedClock(t *testing.T) {
	clk := newClock()
	start := clk.Now()
	slow := func(context.Context) error {
		clk.Advance(150 * time.Millisecond)
		return nil
	}
	a := healthaggregator.New(config{Now: clk.Now})
	register(t, a, check{Name: "slow", Fn: slow})
	got := a.Run(bg)
	if !got.CheckedAt.Equal(start) {
		t.Fatalf("CheckedAt = %v, want %v", got.CheckedAt, start)
	}
	if got.Checks[0].DurationMS != 150 {
		t.Fatalf("DurationMS = %d, want 150", got.Checks[0].DurationMS)
	}

	backwards := func(context.Context) error {
		clk.Advance(-time.Second)
		return nil
	}
	b := healthaggregator.New(config{Now: clk.Now})
	register(t, b, check{Name: "back", Fn: backwards})
	if d := b.Run(bg).Checks[0].DurationMS; d != 0 {
		t.Fatalf("DurationMS = %d, want 0", d)
	}
}

func TestOnCheck(t *testing.T) {
	var seen []string
	record := func(r healthaggregator.CheckResult) { seen = append(seen, r.Name) }
	a := healthaggregator.New(config{OnCheck: record})
	register(t, a, check{Name: "b", Fn: pass}, check{Name: "a", Fn: fail})
	a.Run(bg)
	if !slices.Equal(seen, []string{"a", "b"}) {
		t.Fatalf("seen = %v", seen)
	}

	explode := func(healthaggregator.CheckResult) { panic("callback") }
	b := healthaggregator.New(config{OnCheck: explode})
	register(t, b, check{Name: "x", Fn: pass})
	for range 2 {
		if got := b.Run(bg); got.Status != healthaggregator.StatusUp {
			t.Fatalf("Status = %q", got.Status)
		}
	}
}

func TestRegisterValidation(t *testing.T) {
	tests := []struct {
		name  string
		check check
		want  error
	}{
		{name: "valid", check: check{Name: "db", Fn: pass}},
		{name: "unicode name", check: check{Name: "données", Fn: pass}},
		{name: "max length", check: check{Name: strings.Repeat("a", 64), Fn: pass}},
		{name: "unicode at limit", check: check{Name: strings.Repeat("é", 32), Fn: pass}},
		{name: "empty", check: check{Fn: pass}, want: healthaggregator.ErrInvalidName},
		{name: "too long", check: check{Name: strings.Repeat("a", 65), Fn: pass}, want: healthaggregator.ErrInvalidName},
		{name: "unicode too long", check: check{Name: strings.Repeat("é", 33), Fn: pass}, want: healthaggregator.ErrInvalidName},
		{name: "invalid utf8", check: check{Name: "a\xff", Fn: pass}, want: healthaggregator.ErrInvalidName},
		{name: "nil func", check: check{Name: "x"}, want: healthaggregator.ErrInvalidCheck},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := healthaggregator.New(config{})
			if err := a.Register(tt.check); !errors.Is(err, tt.want) {
				t.Fatalf("Register() = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRegisterDuplicateAndLimit(t *testing.T) {
	a := healthaggregator.New(config{})
	register(t, a, check{Name: "db", Fn: pass})
	if err := a.Register(check{Name: "db", Fn: pass}); !errors.Is(err, healthaggregator.ErrDuplicateCheck) {
		t.Fatalf("Register() = %v, want duplicate", err)
	}
	full := healthaggregator.New(config{})
	for i := range healthaggregator.MaxChecks {
		register(t, full, check{Name: fmt.Sprintf("c%d", i), Fn: pass})
	}
	if err := full.Register(check{Name: "extra", Fn: pass}); !errors.Is(err, healthaggregator.ErrTooManyChecks) {
		t.Fatalf("Register() = %v, want too many", err)
	}
}

func TestErrorType(t *testing.T) {
	tests := []struct {
		err  *healthaggregator.Error
		code string
	}{
		{err: healthaggregator.ErrDuplicateCheck, code: "duplicate_check"},
		{err: healthaggregator.ErrInvalidName, code: "invalid_name"},
		{err: healthaggregator.ErrInvalidCheck, code: "invalid_check"},
		{err: healthaggregator.ErrTooManyChecks, code: "too_many_checks"},
	}
	for _, tt := range tests {
		var target *healthaggregator.Error
		wrapped := fmt.Errorf("wrap: %w", tt.err)
		if !errors.As(wrapped, &target) || target.Code != tt.code {
			t.Fatalf("code for %v = %v, want %q", tt.err, target, tt.code)
		}
		if !strings.HasPrefix(tt.err.Error(), "healthaggregator: ") {
			t.Fatalf("message = %q", tt.err.Error())
		}
	}
}

func TestHandlerStatusCodes(t *testing.T) {
	tests := []struct {
		name   string
		checks []check
		code   int
		status healthaggregator.Status
	}{
		{name: "up", checks: []check{{Name: "db", Fn: pass, Critical: true}}, code: 200, status: healthaggregator.StatusUp},
		{name: "degraded", checks: []check{{Name: "cache", Fn: fail}}, code: 200, status: healthaggregator.StatusDegraded},
		{name: "down", checks: []check{{Name: "db", Fn: fail, Critical: true}}, code: 503, status: healthaggregator.StatusDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := healthaggregator.New(config{})
			register(t, a, tt.checks...)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := serve(a, method)
				if rec.Code != tt.code {
					t.Fatalf("%s code = %d, want %d", method, rec.Code, tt.code)
				}
				if got := rec.Header().Get("Content-Type"); got != "application/json" {
					t.Fatalf("Content-Type = %q", got)
				}
				if got := rec.Header().Get("Cache-Control"); got != "no-store" {
					t.Fatalf("Cache-Control = %q", got)
				}
				if method == http.MethodHead {
					if rec.Body.Len() != 0 {
						t.Fatalf("HEAD body = %q", rec.Body.String())
					}
					continue
				}
				var rep healthaggregator.Report
				if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
				if rep.Status != tt.status {
					t.Fatalf("status = %q, want %q", rep.Status, tt.status)
				}
			}
		})
	}
}

func TestHandlerRejectsOtherMethods(t *testing.T) {
	var calls int32
	a := healthaggregator.New(config{})
	register(t, a, check{Name: "db", Fn: counting(&calls, nil)})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := serve(a, method)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s code = %d", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("Allow = %q", got)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("calls = %d, want 0", n)
	}
}

func TestHandlerJSONShape(t *testing.T) {
	clk := newClock()
	a := healthaggregator.New(config{Now: clk.Now, ErrorDetails: true})
	register(t, a, check{Name: "ok", Fn: pass, Critical: true}, check{Name: "bad", Fn: fail})
	rec := serve(a, http.MethodGet)
	var top map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got := keys(top); !slices.Equal(got, []string{"checked_at", "checks", "status"}) {
		t.Fatalf("top keys = %v", got)
	}
	if top["checked_at"] != "2024-01-02T03:04:05Z" {
		t.Fatalf("checked_at = %v", top["checked_at"])
	}
	items, isList := top["checks"].([]any)
	if !isList || len(items) != 2 {
		t.Fatalf("checks = %v", top["checks"])
	}
	bad, _ := items[0].(map[string]any)
	good, _ := items[1].(map[string]any)
	if got := keys(bad); !slices.Equal(got, []string{"critical", "duration_ms", "error", "name", "status"}) {
		t.Fatalf("failing keys = %v", got)
	}
	if got := keys(good); !slices.Equal(got, []string{"critical", "duration_ms", "name", "status"}) {
		t.Fatalf("passing keys = %v", got)
	}
	if bad["error"] != "boom" {
		t.Fatalf("error = %v", bad["error"])
	}
}

func TestHandlerHidesErrorDetailsByDefault(t *testing.T) {
	leaky := func(context.Context) error {
		return errors.New("dial tcp 10.0.0.5:5432: password authentication failed for user admin")
	}
	for _, details := range []bool{false, true} {
		a := healthaggregator.New(config{ErrorDetails: details})
		register(t, a, check{Name: "db", Fn: leaky, Critical: true})
		body := serve(a, http.MethodGet).Body.String()
		if leaked := strings.Contains(body, "10.0.0.5"); leaked != details {
			t.Fatalf("details=%v leaked=%v body=%s", details, leaked, body)
		}
		if !details && !strings.Contains(body, `"error":"failed"`) {
			t.Fatalf("body = %s", body)
		}
	}
}

func TestHandlerUsesRequestContext(t *testing.T) {
	var calls int32
	a := healthaggregator.New(config{})
	register(t, a, check{Name: "db", Fn: counting(&calls, nil), Critical: true})
	ctx, cancel := context.WithCancel(bg)
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("calls = %d, want 0", n)
	}
}

func TestConcurrentRegisterAndRun(t *testing.T) {
	a := healthaggregator.New(config{CacheTTL: time.Millisecond})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = a.Register(check{Name: fmt.Sprintf("c%02d", i), Fn: pass})
		}()
		go func() {
			defer wg.Done()
			_ = a.Run(bg)
		}()
	}
	wg.Wait()
	if got := a.Run(bg); len(got.Checks) != 20 {
		t.Fatalf("len(Checks) = %d, want 20", len(got.Checks))
	}
}
