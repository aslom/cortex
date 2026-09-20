package costledger

import (
	"context"
	"testing"
	"time"
)

// A WINDOW THAT REACHES PAST RETENTION MUST SAY SO.
//
// This was the one shortfall the ledger disclosed nothing about. An unreadable day is counted, a
// truncated one is counted, a skipped line is counted — and a day whose file prune deleted read
// as a day with no traffic, because readDay cannot tell those apart. So window=month against a
// ledger keeping ten days answered priced:true, short by three weeks, with Caveats clean.
//
// The config floor is nine days while the default is thirty-one precisely so the shipped windows
// work, but a deployment may legitimately keep less history than the longest window asks for. The
// answer is to disclose it, not to forbid it.
func TestQuery_ReportsDaysThatFallOutsideRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.March, 31, 15, 0, 0, 0, time.Local)

	// Ten days of retention, and a month-to-date window: twenty-one of the thirty-one days the
	// window covers are outside what this ledger can hold.
	w := newRetentionWriter(t, dir, 10, now)
	from := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.Local)

	_, caveats, err := w.Query(context.Background(), from, now)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if caveats.DaysBeforeRetention == 0 {
		t.Fatal("DaysBeforeRetention = 0 for a month-to-date window against ten days of " +
			"retention: the answer is short by three weeks and says nothing")
	}
	if caveats.Clean() {
		t.Error("Caveats.Clean() = true, so nothing downstream will disclose the shortfall")
	}
	// Twenty-one days precede the cutoff (31 March back 9 days = 22 March), and all of them are
	// absent because nothing was ever written here.
	if want := int64(21); caveats.DaysBeforeRetention != want {
		t.Errorf("DaysBeforeRetention = %d, want %d (1-21 March fall before the 22 March cutoff)",
			caveats.DaysBeforeRetention, want)
	}
}

// AND IT STAYS QUIET WHEN THE WINDOW FITS, which is what keeps it from becoming noise on an
// always-on indicator. A day inside the retention window with no file is a day with no traffic —
// the common case, and it says nothing at all.
func TestQuery_ReportsNoShortfallForAWindowInsideRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.March, 31, 15, 0, 0, 0, time.Local)

	w := newRetentionWriter(t, dir, 31, now)
	from := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.Local)

	_, caveats, err := w.Query(context.Background(), from, now)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if caveats.DaysBeforeRetention != 0 {
		t.Errorf("DaysBeforeRetention = %d for a month-to-date window against 31 days of "+
			"retention — every day the window covers is retained, so an empty day is a quiet "+
			"day and not a loss", caveats.DaysBeforeRetention)
	}
	if !caveats.Clean() {
		t.Errorf("Caveats = %+v on a window that fits its retention; a clean read must report "+
			"clean or the marker means nothing", caveats)
	}
}

// A FILE THAT OUTLIVED THE CUTOFF IS NOT A LOSS. prune runs at startup and on day roll, so
// between runs a day just outside retention is still on disk and still readable — and counting it
// would report a shortfall that did not happen. Absence is half the test for exactly this reason.
func TestQuery_ADayOutsideRetentionThatSurvivesIsNotCounted(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.March, 31, 15, 0, 0, 0, time.Local)

	// A day file WELL outside retention, written directly and never pruned.
	old := time.Date(2026, time.March, 5, 12, 0, 0, 0, time.Local)
	writeDay(t, dir, old, line(old, "gw", "m", 1, 10, 5, 100))
	w := newRetentionWriter(t, dir, 10, now)

	from := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.Local)
	rows, caveats, err := w.Query(context.Background(), from, now)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("the surviving day's row was not read, so this asserts nothing")
	}
	// 1-21 March precede the cutoff; 5 March is present, so twenty are absent rather than
	// twenty-one.
	if want := int64(20); caveats.DaysBeforeRetention != want {
		t.Errorf("DaysBeforeRetention = %d, want %d — a day outside retention whose file is "+
			"still on disk has not been pruned and is not a loss",
			caveats.DaysBeforeRetention, want)
	}
}

// newRetentionWriter is a Writer with a pinned clock and an explicit retention, which is the pair
// every case here turns on: the cutoff is derived from the clock's day and the retention span.
func newRetentionWriter(t *testing.T, dir string, retainDays int, now time.Time) *Writer {
	t.Helper()
	w, err := New(dir, WithClock(func() time.Time { return now }), WithRetentionDays(retainDays))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}
