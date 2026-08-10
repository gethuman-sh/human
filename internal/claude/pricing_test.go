package claude

import (
	"math"
	"testing"
)

func TestPriceFor(t *testing.T) {
	opus := modelCard.byFamily["opus"].prices()
	sonnet := modelCard.byFamily["sonnet"].prices()
	haiku := modelCard.byFamily["haiku"].prices()
	fallback := modelCard.fallback.prices()

	tests := []struct {
		model string
		want  TokenPrices
	}{
		{"opus 4.8", opus},
		{"opus", opus},
		{"haiku 3.5", haiku},
		// Every shape a caller actually holds resolves to the same row. The raw
		// vendor id is the one that matters most: it is what the cost ledger
		// stores, and keying only on the classified name is what left per-ticket
		// cost billed at opus while the stats panel looked fixed (SC-3580).
		{"claude-sonnet-4-5-20250929", sonnet},
		{"sonnet 4.5", sonnet},
		{"sonnet", sonnet},
		{"us.anthropic.claude-sonnet-4-5-v1:0", sonnet},
		{"claude-3-5-sonnet-20241022", sonnet},
		{"CLAUDE-SONNET-4-5-20250929", sonnet},
		// An unrecognised family prices at the most expensive row, never free.
		{"some-unknown-model", fallback},
		{"gpt-4o", fallback},
		{"", fallback},
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

// The fallback row is read off the card rather than hard-coded, so this test
// keeps asserting "the most expensive row" when the ceiling family changes.
func TestCostUSD_unknownModelFallsBackToFallbackRow(t *testing.T) {
	got := CostUSD("some-unknown-model", 1_000_000, 0, 0, 0)
	want := modelCard.fallback.InputPerM
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("CostUSD(unknown model) = %v, want %v (the %s fallback rate)", got, want, modelCard.fallback.Family)
	}
}
