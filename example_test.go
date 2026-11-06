package healthaggregator_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Trysac/u043-health-aggregator"
)

func ExampleNew() {
	now := func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	agg := healthaggregator.New(healthaggregator.Config{Now: now})
	_ = agg.Register(healthaggregator.Check{Name: "db", Critical: true, Fn: func(ctx context.Context) error { return nil }})
	_ = agg.Register(healthaggregator.Check{Name: "cache", Fn: func(ctx context.Context) error { return errors.New("slow") }})

	rep := agg.Run(context.Background())
	fmt.Println(rep.Status)
	for _, c := range rep.Checks {
		fmt.Println(c.Name, c.Status)
	}
	// Output:
	// degraded
	// cache down
	// db up
}
