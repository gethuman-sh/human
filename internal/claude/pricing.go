package claude

import "strings"

// TokenPrices is the USD cost of one million tokens of each class for a model
// family. The four classes are priced very differently — output is the most
// expensive, cache reads a fraction of fresh input — so a blended count cannot
// be turned into money. This is the single rate card the tool keeps.
type TokenPrices struct {
	InputPerM       float64 // fresh input tokens, USD per 1e6
	OutputPerM      float64 // generated output tokens, USD per 1e6
	CacheCreatePerM float64 // cache-write (context establishment), USD per 1e6
	CacheReadPerM   float64 // cache-read (context re-read), USD per 1e6
}

// modelPrices is keyed by classifyModel's family word ("opus", "sonnet",
// "haiku"). Values are derived from the SC-2549 measured Opus rate card
// (output 105231 tok -> $2.63 => $25/M; cache-write 614446 -> $6.14 => $10/M;
// cache-read 7849469 -> $3.92 => $0.50/M; input = 10x cache-read => $5/M).
// Correct rates here — this block is the only place they live. Families not
// listed fall back to Opus (the most expensive set), so cost is never
// understated; SC-582 may add per-family rows.
var modelPrices = map[string]TokenPrices{
	"opus": {InputPerM: 5.00, OutputPerM: 25.00, CacheCreatePerM: 10.00, CacheReadPerM: 0.50},
}

// opusFallback is the default when a model family has no explicit row.
var opusFallback = modelPrices["opus"]

// PriceFor returns the rate card for a model, given classifyModel's short
// display name ("opus 4.8", "sonnet 4.5") or a raw id. Unknown families fall
// back to Opus rates so an unrecognised model never reads as free.
func PriceFor(model string) TokenPrices {
	family := strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(family, ' '); i >= 0 {
		family = family[:i] // "opus 4.8" -> "opus"
	}
	if p, ok := modelPrices[family]; ok {
		return p
	}
	return opusFallback
}

// CostUSD prices a set of per-class token counts for a model in US dollars.
func CostUSD(model string, input, output, cacheCreate, cacheRead int) float64 {
	p := PriceFor(model)
	return (float64(input)*p.InputPerM +
		float64(output)*p.OutputPerM +
		float64(cacheCreate)*p.CacheCreatePerM +
		float64(cacheRead)*p.CacheReadPerM) / 1_000_000.0
}
