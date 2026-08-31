package llm

import "sync"

// What a call costs, in money rather than tokens.
//
// Z.ai reports no price. The usage object measured in client.go carries token
// counts and nothing else, so the only way to a dollar figure is a rate table
// this package owns. It is hardcoded for the same reason the model id in
// client.go is: the deployment is pinned, so its price is a property of this
// build, not of the environment. Fetching it at runtime would add a network
// dependency, a cache, a staleness window and a fallback table — and the
// fallback table is this file.
//
// Rates are from models.dev (https://models.dev/api.json, provider "zai"),
// which mirrors https://docs.z.ai/guides/overview/pricing.

// nanoUSD is a billionth of a dollar. Money is kept in integers end to end —
// column, wire and arithmetic — because at these rates a whole video costs well
// under a cent, and a float total accumulated over the dozens of calls a
// map-reduce summary makes would drift in exactly the digits being displayed.
//
// A billionth is not arbitrary precision for its own sake: every published
// per-million-token price divides into a whole number of nanodollars per token,
// so the table below is exact and nothing rounds until it is rendered.
type nanoUSD = int64

// rate is what one token costs in each of the three lanes an OpenAI-shaped
// usage object distinguishes: an uncached prompt token, a prompt token the
// endpoint served from its own cache, and a completion token.
type rate struct {
	input  nanoUSD
	cached nanoUSD
	output nanoUSD
}

// ratesNanoPerToken prices each deployment this package can reach. Keyed by the
// wire model id, so the key is the same string modelFrom returns and a new
// deployment cannot be added to client.go without its absence showing up here.
//
// glm-5.3-flash: $0.075 / $0.25 / $0.015 per million tokens in, out and cache
// read. Cache WRITE is free on this model and has no lane; if that ever changes
// it needs a field here AND a token count on the wire to multiply, and Z.ai
// reports no such count today.
var ratesNanoPerToken = map[string]rate{
	model: {input: 75, cached: 15, output: 250},
}

// costNanoUSD prices one call's usage.
//
// The containment rules of an OpenAI-shaped usage object are the whole subtlety
// here, and getting either backwards overcharges by a lot:
//
//   - prompt_tokens INCLUDES cached_tokens. The cached ones are a fifth of the
//     price, so they are subtracted out of the input lane and billed in their
//     own, never added on top.
//   - completion_tokens INCLUDES reasoning_tokens. Reasoning is never priced
//     separately — with thinking permanently on (see thinking.go) it is never
//     zero, so double-counting it would inflate every call peeq makes.
//
// A model with no entry prices at zero. That is "unknown", not "free", which is
// why priced() exists for the caller to say so: an unpriced model means this
// table was not updated alongside client.go, and a confident wrong number would
// be worse than a missing one.
func costNanoUSD(modelID string, u Usage) nanoUSD {
	r, ok := ratesNanoPerToken[modelID]
	if !ok {
		return 0
	}
	// Clamped rather than trusted. cached > prompt cannot happen against a sane
	// endpoint, but both numbers arrive from the wire and a negative input lane
	// would silently CREDIT the video for tokens it actually spent.
	cached := u.CachedTokens
	if cached > u.PromptTokens {
		cached = u.PromptTokens
	}
	if cached < 0 {
		cached = 0
	}
	return (u.PromptTokens-cached)*r.input + cached*r.cached + u.CompletionTokens*r.output
}

// priced reports whether a model id has a rate, so a caller can log the gap
// instead of reporting a zero that reads as free.
func priced(modelID string) bool {
	_, ok := ratesNanoPerToken[modelID]
	return ok
}

// unpricedWarned remembers which model ids have already been complained about.
var unpricedWarned sync.Map

// shouldWarnUnpriced reports whether this is the first call to reach an
// unpriced model, so the gap is said once per process instead of once per call.
//
// Volume is the whole point. The situation it fires in is a deployment added to
// client.go without a rate here, and in that situation EVERY call is unpriced —
// a single video's map-reduce summary would emit one warning per transcript
// chunk, and importing a library would bury the log under thousands of copies
// of one line. Once is enough to act on; the rest is noise that makes the log
// worse at the exact moment someone needs to read it.
func shouldWarnUnpriced(modelID string) bool {
	if priced(modelID) {
		return false
	}
	_, seen := unpricedWarned.LoadOrStore(modelID, true)
	return !seen
}
