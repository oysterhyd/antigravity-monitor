package assets

import _ "embed"

// DefaultPricingJSON holds the canonical built-in model pricing rules.
//
//go:embed pricing.default.json
var DefaultPricingJSON []byte
