package sessionapi

import (
	"testing"
	"time"
)

// daysOutsideRetention is the coverage statement that replaced a false disclosure.
//
// The first version of this counted absent pre-cutoff day FILES and reported them as pruned
// spend under usage.Degraded, whose meaning is "rows are missing from the sum". That cannot be
// known: nothing in costledger records its inception or what prune removed, so an absent old day
// is indistinguishable from a day nobody wrote. A three-day-old install with retention_days=10
// reported twenty-two days of loss and had lost nothing.
//
// So the claim is narrowed to what the server can prove: this window asked for N days the
// configuration does not reach. Whether spend happened on them is unknowable; that the total
// cannot include it is certain.
func TestDaysOutsideRetention(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, time.March, d, 0, 0, 0, 0, time.Local) }
	cutoff := day(22) // retention_days=10 against a clock on the 31st

	for _, tc := range []struct {
		name string
		from time.Time
		want int64
	}{
		// A window that starts inside retention asks for nothing it cannot have.
		{"starts at the cutoff", day(22), 0},
		{"starts inside retention", day(25), 0},
		{"starts today", day(31), 0},
		// Month-to-date on the 31st against ten days: the 1st through the 21st are unreachable.
		{"month to date", day(1), 21},
		{"one day past", day(21), 1},
		{"a week past", day(15), 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := daysOutsideRetention(tc.from, cutoff); got != tc.want {
				t.Errorf("daysOutsideRetention(%s, %s) = %d, want %d",
					tc.from.Format("Jan 2"), cutoff.Format("Jan 2"), got, tc.want)
			}
		})
	}

	// PART OF A DAY IS A WHOLE DAY, because the ledger stores and prunes whole day files: a
	// window reaching back six hours past the cutoff has one date it cannot cover, not a
	// quarter of one.
	if got := daysOutsideRetention(cutoff.Add(-6*time.Hour), cutoff); got != 1 {
		t.Errorf("six hours past the cutoff = %d days, want 1", got)
	}
}

// AND IT SAYS NOTHING ON A FRESH INSTALL, which is the false positive the old design encoded.
//
// A three-day-old ledger and a year-old one have the same horizon, so a request inside that
// horizon reports zero regardless of what is on disk. This is the assertion that would have
// failed the previous implementation.
func TestDaysOutsideRetention_IsSilentForAWindowInsideTheHorizon(t *testing.T) {
	now := time.Date(2026, time.March, 31, 15, 0, 0, 0, time.Local)
	cutoff := time.Date(2026, time.March, 22, 0, 0, 0, 0, time.Local)

	// "today" on a ledger installed three days ago: nothing outside retention, whatever the
	// directory holds.
	if got := daysOutsideRetention(now.Truncate(24*time.Hour), cutoff); got != 0 {
		t.Errorf("window=today reports %d days outside retention on a ten-day ledger; a fresh "+
			"install must be indistinguishable from a long-running one here", got)
	}
	// And 7d, which fits a ten-day retention by three days.
	if got := daysOutsideRetention(now.AddDate(0, 0, -7), cutoff); got != 0 {
		t.Errorf("window=7d reports %d days outside a ten-day retention, which covers it", got)
	}
}
