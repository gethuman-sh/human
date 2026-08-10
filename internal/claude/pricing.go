package claude

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

// PriceFor returns the rate card for a model, whatever shape the caller holds:
// a raw vendor id ("claude-sonnet-4-5-20250929", the shape the cost ledger
// stores), a short display name ("sonnet 4.5", the shape the transcript scan
// stores), or a bare family ("sonnet"). Normalising HERE rather than at each
// caller is the point: when the two callers keyed the card differently, adding
// a sonnet row fixed the stats panel and left per-ticket cost billed at opus
// (SC-3580).
//
// A family the card does not name prices at the card's fallback — the most
// expensive row — so an unrecognised model reads as expensive, never as free.
// Rates themselves live in internal/claude/models.json and nowhere else.
func PriceFor(model string) TokenPrices {
	f, _ := familyFor(model)
	return f.prices()
}

// CostUSD prices a set of per-class token counts for a model in US dollars.
func CostUSD(model string, input, output, cacheCreate, cacheRead int) float64 {
	p := PriceFor(model)
	return (float64(input)*p.InputPerM +
		float64(output)*p.OutputPerM +
		float64(cacheCreate)*p.CacheCreatePerM +
		float64(cacheRead)*p.CacheReadPerM) / 1_000_000.0
}
