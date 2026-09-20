package costledger

import (
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// TestDefaultRetention_CoversEveryDayTheMonthWindowTouches pins this package's retention
// default to the window it has to be able to answer.
//
// TWO CONSTANTS THAT HAVE TO AGREE, in packages that cannot import each other — the same
// relationship config.minCostLedgerRetentionDays has with usage.Window7dLocalDays, and for the
// same reason: this package is imported BY the query path, so taking a dependency on usage
// would invert the direction. The agreement is therefore enforced here, from a test, where the
// import costs nothing.
//
// IT EXISTS BECAUSE THE TWO ALREADY DISAGREED ONCE. The default was 30 while claiming in its
// own doc to be "long enough to answer what did last month cost", and 30 is one day short:
// prune retains [ref-(retainDays-1), ref], which is exactly retainDays distinct dates, so a
// month-to-date window on the 31st of a 31-day month had its first day file already deleted.
// That loss is invisible — a pruned day file is absent rather than unreadable, so it produces
// no Caveats entry and the total comes back labelled "month" and short.
func TestDefaultRetention_CoversEveryDayTheMonthWindowTouches(t *testing.T) {
	if defaultRetentionDays < usage.WindowMonthLocalDays {
		t.Errorf("defaultRetentionDays = %d but a month-to-date window touches up to %d local "+
			"dates (usage.WindowMonthLocalDays) — window=month would answer from %d day files "+
			"and silently omit the rest, with no caveat to disclose it",
			defaultRetentionDays, usage.WindowMonthLocalDays,
			defaultRetentionDays)
	}
	// And not wastefully larger: the default is a month of history, not a quarter. A deployment
	// that wants more says so in config; this is the floor that makes the shipped windows work.
	if defaultRetentionDays > usage.WindowMonthLocalDays {
		t.Errorf("defaultRetentionDays = %d, more than the %d a month window needs — extra "+
			"history is an operator's choice via retention_days, not a default",
			defaultRetentionDays, usage.WindowMonthLocalDays)
	}
}

// TestDefaultRetention_StillCoversTheSevenDayWindow guards the other window against a change
// made for the month's sake. 7d needs nine dates and the month needs thirty-one, so the month
// dominates today — but a future default chosen only against the month could drop below nine
// and break a window nothing in this test file mentions.
func TestDefaultRetention_StillCoversTheSevenDayWindow(t *testing.T) {
	if defaultRetentionDays < usage.Window7dLocalDays {
		t.Errorf("defaultRetentionDays = %d, below the %d dates a 7d window can touch",
			defaultRetentionDays, usage.Window7dLocalDays)
	}
}
