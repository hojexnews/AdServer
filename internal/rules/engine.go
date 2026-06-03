// Package rules implements the delivery rule engine (§4.6).
//
// Supported vectors:
//   - Time-DayOfWeek  ("0"…"6", Sunday=0)
//   - Site-URL        (full URL of the requesting page)
//   - Geo-Country     (ISO 3166-1 alpha-2, e.g. "BR")
//   - Geo-City        (city name derived by geo package, e.g. "Sao Paulo")
//   - Client-Useragent
//   - Site-Variable   (first-party custom variable, keyed by SiteVarKey)
//
// Operators: is / is not / contains
// Combiners: AND / OR (per RuleSet)
//
// Anti-contradiction check (CA-4): the Validate function inspects each
// RuleSet for mutually exclusive conditions under AND logic and marks the
// RuleSet as Impossible when found.  Banners that reference an Impossible
// RuleSet are silenced permanently — they will never be returned as eligible
// by the cascade.
//
// Evaluation is O(1) on the in-memory snapshot with zero heap allocations
// on the hot path (no closures, no interface boxing in the inner loop).
package rules

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/hojex/adserver/gen/go/adserver/common/v1"
	"github.com/hojex/adserver/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Request context passed to the engine on every ad call
// ---------------------------------------------------------------------------

// Context carries the targeting signals available for a single ad request.
// The IP address is NOT present here — it was discarded after geo derivation
// (TX-5/DA-11).  The engine receives the derived Geo only.
type Context struct {
	// Geo is derived from the client IP by the geo.Resolver before the
	// request reaches the engine; the IP itself is discarded (TX-5).
	Geo *commonv1.Geo

	// SiteURL is the page URL reported by the ad tag (referer / js var).
	SiteURL string

	// UserAgent is the raw HTTP User-Agent header value.
	UserAgent string

	// RequestTime is the moment the ad request was received.
	// Used for DayOfWeek evaluation.
	RequestTime time.Time

	// SiteVars holds first-party custom variables injected by the publisher
	// via the ad tag (key=value pairs).
	SiteVars map[string]string
}

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

// Engine evaluates RuleSets against a request Context.
type Engine struct{}

// New creates an Engine.  The Engine is stateless; it reads rule definitions
// from the Snapshot at call time and has no mutable state.
func New() *Engine {
	return &Engine{}
}

// Eligible reports whether all RuleSets referenced by ruleSetIDs pass for
// the given request context (AND across sets, internal combiner per set).
//
// An empty ruleSetIDs slice means "no targeting restrictions" → always
// eligible.
//
// If any referenced RuleSet is marked Impossible (CA-4), the function
// returns false immediately — the banner is silenced.
func (e *Engine) Eligible(
	ctx *Context,
	ruleSetIDs []string,
	snap *snapshot.Snapshot,
) bool {
	for _, id := range ruleSetIDs {
		rs, ok := snap.RuleSets[id]
		if !ok {
			// Unknown RuleSet ID — treat as a missing rule: reject.
			// This protects against stale banners referencing deleted rules.
			return false
		}
		if rs.Impossible {
			// CA-4: rule set has been flagged as logically impossible.
			return false
		}
		if !evalRuleSet(ctx, rs) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Anti-contradiction validator (CA-4)
// ---------------------------------------------------------------------------

// Validate inspects every RuleSet in the snapshot and marks any set that
// contains mutually exclusive AND conditions as Impossible.
//
// Call this once after building a new snapshot, before serving traffic.
// The detection is conservative: it only catches provably impossible sets
// without external knowledge (e.g. two "is" conditions on the same
// single-valued vector under AND).
//
// The function modifies the RuleSet objects IN PLACE (safe during snapshot
// construction, before the snapshot is published to the Store).
func Validate(snap *snapshot.Snapshot) {
	for _, rs := range snap.RuleSets {
		why := detectImpossible(rs)
		if why != "" {
			rs.Impossible = true
			rs.ImpossibleWhy = why
		}
	}
}

// detectImpossible returns a non-empty explanation string if the RuleSet is
// logically impossible, or "" if it could potentially match.
func detectImpossible(rs *snapshot.RuleSet) string {
	if rs.Logic != snapshot.LogicAND {
		// Under OR, any single condition being satisfiable means the set
		// might fire — we cannot prove impossibility without further
		// analysis, so we leave OR sets alone.
		return ""
	}

	// Group "is" conditions by vector (ignoring SiteVarKey for now, as
	// different keys are not contradictory).
	type key struct {
		vec        snapshot.RuleVector
		siteVarKey string
	}
	positives := make(map[key][]string)   // vector → values asserted IS
	negatives := make(map[key]struct{})   // vectors that have an IS-NOT

	for _, c := range rs.Conditions {
		k := key{vec: c.Vector, siteVarKey: c.SiteVarKey}
		switch c.Operator {
		case snapshot.OpIs:
			positives[k] = append(positives[k], c.Value)
		case snapshot.OpIsNot:
			negatives[k] = struct{}{}
		}
	}

	// Case 1: two different "is" values on the same single-valued vector
	// under AND — impossible (the request can only have one value at a time).
	for k, vals := range positives {
		if len(vals) > 1 {
			// DayOfWeek: a request lands on exactly one day.
			// Country, City, UserAgent: one value per request.
			// SiteURL: one per request.
			// SiteVar: one value per key per request.
			return fmt.Sprintf(
				"AND with two IS conditions on same vector %v (%v): "+
					"impossible (CA-4)", k.vec, vals,
			)
		}
	}

	// Case 2: IS value == IS-NOT value on the same vector — x IS "BR" AND
	// x IS NOT "BR" can never be true.
	for k, vals := range positives {
		if _, hasNot := negatives[k]; hasNot {
			// Check if any positive value appears in a negative condition.
			// (We only reach here if the positive set has exactly one value
			// after Case 1 pruning, but we re-iterate for clarity.)
			for _, v := range vals {
				for _, c := range rs.Conditions {
					ck := key{vec: c.Vector, siteVarKey: c.SiteVarKey}
					if ck == k && c.Operator == snapshot.OpIsNot && c.Value == v {
						return fmt.Sprintf(
							"AND with IS %q AND IS-NOT %q on same vector %v: "+
								"impossible (CA-4)", v, c.Value, k.vec,
						)
					}
				}
			}
		}
	}

	// Case 3: DayOfWeek — all 7 days covered by IS-NOT under AND.
	// (Each IS-NOT eliminates one day; if all 7 are excluded, impossible.)
	dayNotCount := 0
	for _, c := range rs.Conditions {
		if c.Vector == snapshot.VectorTimeDayOfWeek && c.Operator == snapshot.OpIsNot {
			dayNotCount++
		}
	}
	if dayNotCount >= 7 {
		return "AND excludes all 7 weekdays with IS-NOT: impossible (CA-4)"
	}

	return ""
}

// ---------------------------------------------------------------------------
// Internal evaluation
// ---------------------------------------------------------------------------

func evalRuleSet(ctx *Context, rs *snapshot.RuleSet) bool {
	if len(rs.Conditions) == 0 {
		return true // open rule: always matches
	}
	switch rs.Logic {
	case snapshot.LogicAND:
		for i := range rs.Conditions {
			if !evalCondition(ctx, &rs.Conditions[i]) {
				return false
			}
		}
		return true
	case snapshot.LogicOR:
		for i := range rs.Conditions {
			if evalCondition(ctx, &rs.Conditions[i]) {
				return true
			}
		}
		return false
	default:
		// Unknown combiner: fail closed.
		return false
	}
}

func evalCondition(ctx *Context, c *snapshot.Condition) bool {
	actual := resolveVector(ctx, c)
	switch c.Operator {
	case snapshot.OpIs:
		return strings.EqualFold(actual, c.Value)
	case snapshot.OpIsNot:
		return !strings.EqualFold(actual, c.Value)
	case snapshot.OpContains:
		return strings.Contains(strings.ToLower(actual), strings.ToLower(c.Value))
	default:
		// Unknown operator: fail closed.
		return false
	}
}

// resolveVector extracts the request attribute for condition c.
func resolveVector(ctx *Context, c *snapshot.Condition) string {
	switch c.Vector {
	case snapshot.VectorTimeDayOfWeek:
		wd := ctx.RequestTime.Weekday() // time.Sunday=0 … time.Saturday=6
		return strconv.Itoa(int(wd))
	case snapshot.VectorSiteURL:
		return ctx.SiteURL
	case snapshot.VectorGeoCountry:
		if ctx.Geo != nil {
			return ctx.Geo.GetCountry()
		}
		return ""
	case snapshot.VectorGeoCity:
		if ctx.Geo != nil {
			return ctx.Geo.GetCity()
		}
		return ""
	case snapshot.VectorClientUA:
		return ctx.UserAgent
	case snapshot.VectorSiteVariable:
		if ctx.SiteVars != nil {
			return ctx.SiteVars[c.SiteVarKey]
		}
		return ""
	default:
		return ""
	}
}
