// Package policy maps a request to the rate limit that applies to it.
// rules constrain tier and/or endpoint and the most specific match wins,
// with endpoint outranking tier; a default covers everything else
package policy

import (
	"fmt"
	"slices"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// the attributes a limit can be resolved on
type Request struct {
	Tier     string
	Endpoint string
}

// a resolved limit: which algorithm to run and its config
type Limit struct {
	Algorithm limiter.Algorithm
	Config    limiter.Config
}

// matches requests by tier and/or endpoint; an empty field matches anything
type Rule struct {
	Tier     string
	Endpoint string
	Limit    Limit
}

// endpoint outranks tier so a per-endpoint override beats a tier default
func (r Rule) specificity() int {
	s := 0
	if r.Tier != "" {
		s++
	}
	if r.Endpoint != "" {
		s += 2
	}
	return s
}

func (r Rule) matches(req Request) bool {
	if r.Tier != "" && r.Tier != req.Tier {
		return false
	}
	if r.Endpoint != "" && r.Endpoint != req.Endpoint {
		return false
	}
	return true
}

// immutable rule set ordered most specific first, with a default fallback
type Policies struct {
	def   Limit
	rules []Rule
}

// validates every limit and rejects duplicate matchers, which would make
// resolution ambiguous
func NewPolicies(def Limit, rules ...Rule) (*Policies, error) {
	if err := validateLimit(def); err != nil {
		return nil, fmt.Errorf("policy: default: %w", err)
	}

	seen := make(map[[2]string]bool, len(rules))
	for _, r := range rules {
		key := [2]string{r.Tier, r.Endpoint}
		if seen[key] {
			return nil, fmt.Errorf("policy: duplicate rule for tier %q endpoint %q", r.Tier, r.Endpoint)
		}
		seen[key] = true
		if err := validateLimit(r.Limit); err != nil {
			return nil, fmt.Errorf("policy: rule for tier %q endpoint %q: %w", r.Tier, r.Endpoint, err)
		}
	}

	ordered := slices.Clone(rules)
	slices.SortStableFunc(ordered, func(a, b Rule) int {
		return b.specificity() - a.specificity()
	})
	return &Policies{def: def, rules: ordered}, nil
}

func validateLimit(l Limit) error {
	if !l.Algorithm.Valid() {
		return fmt.Errorf("unknown algorithm %q", l.Algorithm)
	}
	return l.Config.Validate()
}

// the limit that applies to req: the most specific matching rule, else the
// default. rules are pre-sorted, and within one specificity class at most
// one rule can match, so the result is independent of registration order
func (p *Policies) Resolve(req Request) Limit {
	for _, r := range p.rules {
		if r.matches(req) {
			return r.Limit
		}
	}
	return p.def
}
