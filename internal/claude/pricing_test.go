package claude

import (
	"math"
	"testing"
)

func TestPriceFor(t *testing.T) {
	tests := []struct {
		model string
		want  TokenPrices
	}{
		{"opus 4.8", modelPrices["opus"]},
		{"opus", modelPrices["opus"]},
		// Unknown families fall back to Opus so cost is never understated.
		{"sonnet 4.5", opusFallback},
		{"haiku 3.5", opusFallback},
		{"some-unknown-model", opusFallback},
		{"", opusFallback},
	}
	for _, tt := range tests {
		got := PriceFor(tt.model)
		if got != tt.want {
			t.Errorf("PriceFor(%q) = %+v, want %+v", tt.model, got, tt.want)
		}
	}
}

func TestCostUSD(t *testing.T) {
	// The ticket's measured Opus example: input 278, output 105231,
	// cacheCreate 614446, cacheRead 7849469.
	got := CostUSD("opus 4.8", 278, 105231, 614446, 7849469)
	want := (278*5.00 + 105231*25.00 + 614446*10.00 + 7849469*0.50) / 1_000_000.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("CostUSD = %v, want %v", got, want)
	}
}

func TestCostUSD_zero(t *testing.T) {
	got := CostUSD("opus 4.8", 0, 0, 0, 0)
	if got != 0 {
		t.Errorf("CostUSD(all zero) = %v, want 0", got)
	}
}

func TestCostUSD_unknownModelFallsBackToOpus(t *testing.T) {
	got := CostUSD("some-unknown-model", 1_000_000, 0, 0, 0)
	want := opusFallback.InputPerM
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("CostUSD(unknown model) = %v, want %v (opus fallback rate)", got, want)
	}
}
