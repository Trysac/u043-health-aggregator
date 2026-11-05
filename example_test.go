package healthaggregator_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	healthaggregator "github.com/Trysac/u043-health-aggregator"
)

func ExampleNew() {
	agg := healthaggregator.New(healthaggregator.Config{DefaultTimeout: time.Second, CacheTTL: 5 * time.Second})

	_ = agg.Register(healthaggregator.Check{
		Name:     "database",
		Critical: true,
		Fn:       func(ctx context.Context) error { return nil },
	})
	_ = agg.Register(healthaggregator.Check{
		Name: "cache",
		Fn:   func(ctx context.Context) error { return errors.New("connection refused") },
	})

	report := agg.Run(context.Background())
	fmt.Println(report.Status)
	for _, c := range report.Checks {
		fmt.Println(c.Name, c.Status, c.Critical)
	}
	// Serve it with: http.Handle("/readyz", agg.Handler())

	// Output:
	// degraded
	// cache down false
	// database up true
}
