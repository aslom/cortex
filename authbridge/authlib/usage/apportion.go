package usage

import "github.com/rossoctl/cortex/authbridge/authlib/pricing"

// ApportionTiers splits CostMicros across the rate tiers by the modelled mix.
//
// THE MIX IS THE RATE TABLE'S, THE MAGNITUDE IS THE GATEWAY'S. A request priced from a
// response header has an authoritative total and no breakdown, because no gateway publishes
// one — so the modelled figures cannot be shown as dollars beside it without putting two
// disagreeing totals on screen. Used as a ratio they answer the question anyway, and the
// column adds up to the headline a reader checks first.
//
// ok is false when there is no mix (nothing to apportion by) or no usable total (nothing to
// apportion). Callers render the "not known here" cell — NOT four zeros, which would report
// the traffic as free, and not a guess.
//
// NO COVERAGE THRESHOLD, deliberately. A mix drawn from one request of forty is a weak key,
// but every particular floor is a number nobody can defend — 50% and 10% are equally
// arbitrary — and a constant whose value is unjustifiable is worse than the behaviour it
// guards. The display marks every figure inexact instead, and a reader who wants coverage
// has `abctl cost`, which reports priced against priceable already.
//
// THE ONE PLACE THIS ARITHMETIC LIVES. The drawer, the text command and the JSON all call
// it, so three surfaces cannot disagree about a figure derived three times.
func (c Counts) ApportionTiers() (tiers [pricing.NumTiers]int64, ok bool) {
	mix := [pricing.NumTiers]int64{
		pricing.TierInput:      c.InputCostMicros,
		pricing.TierCacheWrite: c.CacheWriteCostMicros,
		pricing.TierCacheRead:  c.CacheReadCostMicros,
		pricing.TierOutput:     c.OutputCostMicros,
	}
	var mixTotal int64
	for _, v := range mix {
		if v > 0 {
			mixTotal += v
		}
	}
	// A negative total is not a total, the refusal every money surface in this repo makes.
	if mixTotal <= 0 || c.CostMicros <= 0 {
		return tiers, false
	}

	// THE RATIO IN FLOAT, THE RESULT BOUNDED BY THE TOTAL. The integer form,
	// CostMicros*mix[i]/mixTotal, overflows int64 on the MULTIPLY for a large total against a
	// large mix — and both are sums over a whole window, so neither is small. Scaling keeps
	// every intermediate at or under CostMicros, a quantity already known to fit.
	var sum int64
	largest := -1
	for i, v := range mix {
		if v <= 0 {
			// ABSENT FROM THE MIX MEANS ABSENT FROM THE ANSWER. A tier that priced nothing
			// gets nothing, rather than a share of the rounding: inventing a cache-write
			// charge for traffic that wrote no cache is the kind of small lie this surface
			// refuses everywhere else.
			continue
		}
		tiers[i] = int64(float64(c.CostMicros) * (float64(v) / float64(mixTotal)))
		sum += tiers[i]
		if largest < 0 || v > mix[largest] {
			largest = i
		}
	}
	// THE REMAINDER GOES TO THE LARGEST TIER. Four truncations need not sum to one total, and
	// the display's whole claim is that they do. Largest because that is where a micro is
	// proportionally smallest and cannot flip a rank — giving it to the smallest tier could
	// reorder the bars, which is a visible lie in the service of an invisible one.
	if r := c.CostMicros - sum; r != 0 && largest >= 0 {
		tiers[largest] += r
	}
	return tiers, true
}
