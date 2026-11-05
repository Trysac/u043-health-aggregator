package healthaggregator_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Trysac/u043-health-aggregator"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{name: "trims and lower-cases", input: "  Hello World ", want: "hello world"},
		{name: "already normal", input: "abc", want: "abc"},
		{name: "keeps inner spacing", input: "A  B", want: "a  b"},
		{name: "tabs and newlines are whitespace", input: "\tX\n", want: "x"},
		{name: "empty is rejected", input: "", wantErr: healthaggregator.ErrBlank},
		{name: "whitespace only is rejected", input: " \t\n ", wantErr: healthaggregator.ErrBlank},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := healthaggregator.Normalize(tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Normalize(%q) error = %v, want %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestErrBlankMessage(t *testing.T) {
	if got, want := healthaggregator.ErrBlank.Error(), "healthaggregator: input is blank"; got != want {
		t.Fatalf("ErrBlank.Error() = %q, want %q", got, want)
	}
}

func ExampleNormalize() {
	out, _ := healthaggregator.Normalize("  Hello World ")
	fmt.Println(out)

	_, err := healthaggregator.Normalize("   ")
	fmt.Println("rejected:", err)
	// Output:
	// hello world
	// rejected: healthaggregator: input is blank
}
