# Budget Spans in the abctl Spend Band

Status: proposed · 2026-09-20 · targets `authbridge/authlib/{usage,config,costledger}`,
`authbridge/cmd/abctl/{apiclient,tui}` · **builds on PR #1071 (`fix/header-align`), see §3.9**

## 1. Problem

An operator watching agent spend budgets against four spans: the **last hour** (is
something running away right now), **today**, **the last week**, and **the month to
date** (which is when the budget resets). abctl can reach two of them, reports a third
by accident, and cannot express the fourth at all.

Four defects, found by reading one live screen:

```
abctl · http://127.0.0.1:47601 · [Sessions] Pipeline · lifetime totals
TODAY     LAST 1H  SAVED     CACHE HIT  TOKENS
$18.7994  $4.0405  ~$0.6037  97%        8.3M
SESSION         UPDATED   EVENTS  TOKENS      COST       SAVED     ACTIVE
5e1e9a28-078c…  18s ago       67    2.6M  $1.7107   ~$0.1467  ●
ecb7387f-bffd…  2m ago        26    5.7M  $2.3297   ~$0.0353
default         8m ago         1       —        —          —
```

**1a. The band mixes two spans and labels only two of five cells.** `TODAY` is
ledger-backed and covers the local day. `LAST 1H` is the ring's rolling hour. But
`CACHE HIT` and `TOKENS` are read off the *window* snapshot too (`spend.go:491,493`) —
so `8.3M` is the last hour's token count, sitting fifth in a row that opens with
`TODAY`. A reader has no way to know. There is no cell that states its own span unless
its label happens to contain one.

**1b. The month is unreachable, and the week is not what a budget means.**
`ParseWindowSpec` accepts exactly two symbolic windows, `today` and `7d`
(`snapshot.go:715-724`). There is no month-to-date. `ParseWindow` refuses any duration
over `MaxWindow` (`snapshot.go:593`), and `MaxWindow` is six hours
(`NumBuckets=360 × BucketWidth=1m`, `usage.go:36,41`), so a caller cannot smuggle a
month in as a duration either — correctly, but it leaves the span with no spelling.

**1c. The scope note was misleading in the scary direction.** `sessionsScopeNote`
(`sessions_pane.go:414`) appends `· lifetime totals` to the title to say the table's
figures are per-session sums rather than time slices. Two problems. It sits above a band
whose two nearest cells are explicitly time-windowed, so it reads as covering them. And
"lifetime" implies the longest span on screen when it is usually the shortest: the
session store is in-memory and resets on proxy restart (`sessions_pane.go:38-43`,
`session/store.go:578`), so on the screen above, the table's `COST` column sums to
`$4.04` — matching `LAST 1H` almost exactly — while `TODAY` reads `$18.80`. The label
that sounds like "everything ever" was describing about one hour.

**1d. The figures are hard to read.** Four decimals on every money cell
(`formatUSDCell` → `formatUSD4`, `prune_saving.go:86,101`) is finer than any decision
needs. `SAVED` wears `~` on every value unconditionally (`sessions_pane.go:297`,
`spend_band.go:75`), which carries no per-row information and dilutes the same glyph
where it *is* conditional. And the band is flat: `styleMuted` labels over unstyled
values, no hierarchy between the figure you act on and the ones you skim.

A fifth thing is worth stating because it is **not** a defect and must not be "fixed":
the band is global — every poll passes `""` for the session (`spend.go:690,825`), and
`spendStripVisible` draws it on every pane but the two pickers (`spend_strip.go:697`).
So it does not change when you drill into a session. It structurally cannot: the ledger
row key is `(endpoint, model, agent, provenance)` with no session dimension by design,
and the server *rejects* `session=` alongside a symbolic window
(`sessionapi/usage.go:189`). The fix is to make the band's scope legible, never to scope
it per session.

## 2. What exists today

Verified against `a62664bc`, not assumed.

| Thing | Today | Needed |
|---|---|---|
| `usage.ParseWindowSpec` symbolic windows | `today`, `7d` | + `month` |
| `usage.StartOfLocalDay` | yes, DST-safe sweep | + `StartOfLocalMonth` |
| Ring `MaxWindow` | 6h (`360 × 1m`) | unchanged |
| `costledger` default retention | 30 days (`store.go:25`) | 32 (see §3.8) |
| `config` retention floor / ceiling | 9 / 3650 (`config.go:122,140`) | floor unchanged |
| `apiclient.GetUsageWindow` | takes a **string** window | unchanged — already right |
| Ledger path grouping | `ledgerSnapshot(ctx, spec, group)` | unchanged — already right |
| `spendDrawerWindows` | `{15m, 1h, 6h}` (`time.Duration`) | `{1h, today, 7d, month}` (string) |
| `spendDrawerAxes` | model, endpoint, agent | unchanged |
| `formatWindowLabel` | takes `time.Duration` | takes a span string |
| `spendDrawerLines` | `numTierRows + 2` = 6 | unchanged |
| `tierColumnWidth` / two-column min | 34 / 72 | unchanged |
| Band poll chains | 2 (window, today) | 4 band + 1 drawer (§3.4) |
| `headerTitle` (#1071) | strips alignment padding | + strips the column marker (§3.7) |
| `sessionsRightAligned` (#1071) | keyed on bare titles | unchanged, but asserted (§5) |
| `renderSpendBand` alignment | left-flush, `%-*s` | right-aligned, uniform width (§3.3) |

Two facts do most of the work in the design below:

- **`GetUsageWindow` already takes a string window**, and its doc says why: *"a symbolic
  window the server names — `today`, `7d` — which a `time.Duration` cannot express"*
  (`client.go:470`). `GetUsage` is a thin duration-stringifying wrapper over it. So
  widening `w` to symbolic spans needs no new client surface.
- **The ledger can already fold by every axis `a` offers.** `ledgerSnapshot` takes
  `group`, and the ledger's row key is `(endpoint, model, agent, provenance)` — exactly
  the three axes in `spendDrawerAxes`. A per-model breakdown of the month is a query the
  storage is already shaped for.

## 3. Design

### 3.1 A `month` symbolic window

Add `WindowMonth = "month"` to `ParseWindowSpec`, returning
`Spec{Label: "month", From: StartOfLocalMonth(now), To: now}`.

`month` rather than `mtd`: `today` is already a **boundary word, not a length**, and
`month` joins that family. `mtd` is less ambiguous read cold but introduces a second
naming convention on the same enum. The label is echoed back on the wire, so a client
always learns which window it got.

`StartOfLocalMonth` mirrors `StartOfLocalDay` exactly, including the forward sweep from
a noon anchor (`snapshot.go:602-672`): take the first local date of `t`'s month, anchor
at `dayAnchorHour`, then sweep back to the first instant that exists on that date. The
sweep is what handles a zone whose DST transition lands at 00:00 on the 1st, where
"midnight on the first" either does not exist or occurs twice. Reusing the existing
sweep rather than writing a second one is the point — that comment records a defect this
would otherwise reintroduce.

Rolling `7d` is kept as-is. It is inconsistent with a calendar-aligned month, and
`snapshot.go:760-766` already records that making `7d` calendar-aligned is a product
decision with a wire-visible step change. Out of scope here; noted in §7.

### 3.2 `w` cycles exactly the four budget spans

```go
var spendDrawerWindows = []string{"1h", usage.WindowToday, usage.Window7d, usage.WindowMonth}
```

`15m` and `6h` are dropped. They are ring-diagnostic spans, not budget spans, and the
whole surface reads better with four entries a reader recognises than six they have to
filter.

This **removes** a documented footgun rather than adding one. `windowStep` is currently
an *offset* from `spendDrawerWindowDefault` specifically because `spendWindow` (1h) sits
in the middle of an ascending slice, so a zero value read as an index would silently
move the band's own poll to 15m (`spend.go:127-132`). With `1h` first, zero is the
correct default and `windowStep` becomes a plain index. Delete
`spendDrawerWindowDefault` and the modulus-with-offset arithmetic.

`formatWindowLabel(time.Duration)` becomes span-string-based. It is still needed:
`15m`/`1h` want display forms, and `today`/`7d`/`month` want upper-cased words.

The comment at `spend_drawer.go:83-89` that excluded `today` and `7d` from `w` is now
wrong on both of its reasons, and should be replaced rather than deleted silently:

- *"would render the same number twice"* — true when the band had a single `TODAY`
  headline and the drawer had no other span. Under §3.3 the drawer shows a **breakdown**
  the band cannot show at any span.
- *"a second ledger query on a keypress"* — the real hazard was a ledger read on the
  20-second poll loop, not on user demand. `abctl cost --window 7d` already does exactly
  this read on demand from a shell.

### 3.3 The band is the span row, and the drawer's selector

The band becomes **cost-only, four cells, one per span**, in ascending order, at a
**uniform cell width with both lines right-aligned**:

```
LAST 1H    TODAY   7 DAYS    MONTH
  $4.04   $18.80  $216.44  $703.18
```

Uniform width is the widest label or value across all four cells — 7 here — so the four
figures sit on a fixed stride and their decimal points share a column. 34 columns total,
narrower than today's five-cell band.

**Right-aligned, not left.** `renderSpendBand` currently pads both lines with `%-*s`
(`spend_band.go:111-112`), which left-flushes every figure in its cell. That is the same
defect PR #1071 fixes in the two tables, and the band is the one money surface that PR
does not touch — see §3.9. Four costs of different magnitudes that a reader compares
directly is precisely the case that needs the last digits in one place. Cell width stays
`max(label, value)` per cell for the *drop* arithmetic; the uniform width is applied to
the cells that survive.

The invariant this establishes is **every band cell's label names its span**, and that is
what **structurally eliminates defect 1a** — not cost-only-ness as such. Cost-only
satisfies it trivially; any future cell must satisfy it too.

`SAVED`, `CACHE HIT` and `TOKENS` come off the band. The information does not disappear:
the drawer's tier column says the cache story better than one percentage can
(`cache-read` against `input`, with bars), the sessions table keeps per-session `TOKENS`
and `SAVED`, and the Usage pane and `abctl cost` keep the full metric set.

When the drawer is open, the selected span's cell is emphasised and the drawer below
breaks that span down. The band becomes the drawer's tab bar:

```
LAST 1H   TODAY     7 DAYS     MONTH          ← MONTH emphasised
$4.04     $18.80    $216.44    $703.18

  WHERE IT WENT                     BY MODEL
input       ███████████ $281.27     claude-opus-5    $402.11
output      ██████████ $272.37      claude-sonnet-5  $211.03
cache-write ███ $98.44              claude-haiku-4.5  $68.92
cache-read  ██ $51.10               (other)           $21.12
[a] by model  [w] month  esc closes
```

The drawer's shape is unchanged: `spendDrawerLines` stays `numTierRows + 2`,
`tierColumnWidth` stays 34, the two-column threshold stays 72, tiers stay in the left
column. No new key, no mode bit, nothing displaced.

**The emphasis appears only while the drawer is open.** `w` is gated on
`spendDrawerVisible()` (`keys.go:174-177`), so a persistent highlight would advertise a
selection the user cannot change. With the drawer closed the band is four plain totals
under the §3.7 hierarchy.

**Drop order must not follow visual order.** `renderSpendBand` drops whole cells
right-to-left with `TODAY` outliving the rest (`spend_band.go:44-46`), which on an
ascending row would drop `MONTH` — the budget figure — first. Keep the visual order
ascending and give the fitter an explicit priority: drop `7 DAYS`, then `LAST 1H`, so
`TODAY` and `MONTH` survive longest. Cells still drop whole and nothing is ever clipped.

### 3.4 Decouple the band's polls from the drawer's

Today one poll serves both: *"one poll serves both, the strip reading Totals and the
drawer reading Series"* (`spend.go:681`). A side effect is that pressing `w` changes what
the band's window cell reports. With fixed span cells that coupling has to go.

Generalise the existing two-chain pattern (`window` and `today` already have separate
snapshots, errors, `reqSeq` and `tickGen` — split precisely because *"the two can fail
independently"*) into a table-driven set:

| Chain | Window | Group | Cadence | Why |
|---|---|---|---|---|
| band 1h | `1h` | none | 20s | ring-served, free |
| band today | `today` | none | 60s | 1 day file |
| band 7d | `7d` | none | 5 min | ≤9 day files |
| band month | `month` | none | 5 min | ≤31 day files |
| drawer | `w`-selected | `a`-selected | on keypress, then 5 min | only while open |

Long spans on a slow cadence is not a compromise — a month-to-date total moves by well
under 0.1% in twenty seconds. The band chains request `group=none` because nothing reads
their `Series`; only the drawer needs a fold, and only for the one span on screen.

Per-chain failure isolation is a requirement, not a nicety: the existing code carries a
fix for a wedged window poll discarding a good day total (`spend.go:406-413`). Four
chains must not be able to blank each other.

### 3.5 Disclose a span the deployment cannot answer

With no cost ledger — *"Kubernetes by design"* — the handler serves symbolic windows from
the ring instead, clamped to the window asked for (`sessionapi/usage.go:~200-212`). The
ring holds six hours. So `today`, `7d` and `month` all degrade, and `month` degrades by a
factor of about 120.

The server already reports the window it actually served, and `spendSummary` already
reads it (`WindowLabel: sanitizeLabel(snap.Window)`). So the band can compare requested
against served and render `emptyCell` (`—`) when they differ:

```
LAST 1H   TODAY   7 DAYS   MONTH
$4.04     —       —        —
```

`—` rather than dropping the cell, because silence about a span the operator is watching
is worse than an explicit "not here". `—` rather than a number, because a six-hour figure
under a `MONTH` label is the "wrong number wearing a right label" this package refuses
everywhere else. The footer or hint line should say why once, rather than per cell.

### 3.6 Money to cents

`formatUSDCell` renders two decimals instead of four, and its floor becomes `<$0.01`
instead of `<$0.0001`. The floor's reason is unchanged and still load-bearing: never
render a real charge as `$0.00`.

`sessionMoneyCell`'s precision ladder (`sessions_pane.go:352-371`) loses its four-decimal
rung and starts at two, keeping the rungs below it and keeping the rule that a rung which
rounds a real charge to zero is skipped rather than printed.

Blast radius: 19 non-test call sites, and **153 test assertions carrying four-decimal
dollar literals**. Mechanical, but it is the bulk of the diff and belongs in its own
commit.

`abctl cost` is included. One spelling everywhere is this package's standing rule, and a
reconciliation report that disagrees with the TUI by two decimal places is worse than
either precision alone.

### 3.7 `~` moves to the label; visual hierarchy

`inexactMarker` comes off `SAVED` *values* and goes onto the sessions table's column
heading — `SAVED ~`. It is applied unconditionally (`sessions_pane.go:297`), so per row it
carries no information while diluting the same glyph on cost figures where it *is*
conditional. This makes `~` on a money figure mean something again.

The band's own `SAVED` cell (`spend_band.go:75`) wears the marker unconditionally for the
same reason, and needs no treatment here: §3.3 removes that cell, in the same PR (§4).

**The sessions column title cannot simply be renamed.** PR #1071 makes the title string
do double duty — it is both the rendered heading and the lookup key — and `headerTitle`
is `strings.TrimSpace(c.Title)`, which strips alignment padding but nothing else. A title
of `"SAVED ~"` therefore breaks three things at once, and only the first is loud:

| Site | Effect of a renamed title |
|---|---|
| `sessionsColumnWidth(want, "SAVED")` (`sessions_pane.go:141`) | returns 0 → `sessionMoneyCell` budget 0 → **every saving renders `—`** |
| `sessionsRightAligned["SAVED"]` (`sessions_pane.go:65`) | false → heading silently stops being right-aligned, reintroducing #1071's bug |
| `TestSessionsHeader_SitsOverItsOwnValues` | `continue`s past the column (`header_align_test.go:266`) → **passes anyway** |

So `headerTitle` must strip the marker as well as the padding:

```go
// headerMarker qualifies EVERY figure in a column, so it is a property of the heading
// and not of any cell. Stripped here for the same reason alignment padding is: the name
// is the key, and this is a rendering detail.
const headerMarker = " ~"

func headerTitle(c table.Column) string {
    return strings.TrimSuffix(strings.TrimSpace(c.Title), headerMarker)
}
```

Order matters — `TrimSpace` first to remove the leading alignment pad, then the suffix.
This extends `headerTitle`'s existing contract rather than bending it; its own doc already
says *"Trimming here keeps the name the key and the padding a rendering detail, which is
what it is."* An unconditional column-level marker is the same class of thing.

One width consequence: `SAVED ~` needs 7 display columns where `SAVED` needed 5, so the
heading becomes the binding constraint on the column's minimum instead of the cell. With
§3.6's cents the narrowest honest cell is `<$0.01` at 6, so the money column's floor must
be `max(sessionMoneyCellMin, len("SAVED ~"))` = 7. Still **narrower than today's 9**
(`~<$0.0001`), so the change buys width rather than spending it — but the floor has to be
computed from both halves, not just the cell, or the fitter will squeeze the column until
bubbles truncates the heading to `SAVED…`.

`+` (`partialMarker`) and `!` (`damagedMarker`) stay on figures. They are conditional,
and their whole design is that they ride on the number
(`spend_strip.go:44-48,82-88`).

Dropping the marker narrows `sessionMoneyCellMin` from 9 (`~<$0.0001`) to the new
two-decimal floor.

Visual hierarchy, using the palette already in `styles.go` — no new colors:

- Band: `TODAY` and `MONTH` carry weight; `LAST 1H` and `7 DAYS` stay default. The
  `w`-selected cell takes `colorAccent` + bold while the drawer is open.
- `ACTIVE ●` becomes `colorOK`; a row with no activity and no cost recedes to
  `styleMuted` (the `default` row in the screen above).
Item F from the original sketch — "style the tab strip" — is **dropped**. `viewTabs`
(`app.go:1706`) already renders the active tab through `styleTitle` and the inactive one
through `styleHint`. The only change left would be removing the literal `[ ]`, and those
brackets are what still distinguishes the tabs on a terminal with no color, so they stay.

These are style-only and add no columns, so no fitting arithmetic changes. Tests that
assert rendered text need escape-aware comparison, which is why this is its own commit.

### 3.8 Retention

`defaultRetentionDays` is 30 (`costledger/store.go:25`), so on the 31st of a 31-day month
the ledger is one day file short of answering `month`. Raise the default to 32 — one
extra file, roughly 300 KB at the volumes this ledger is sized for.

The floor (`minCostLedgerRetentionDays = 9`) stays where it is, derived from
`Window7dLocalDays`. Deliberately **not** raised to 32: that would make a 9-day
deployment invalid for a window it never asked for. Instead, a `month` query whose span
reaches past retention is a *partial* answer and must wear `partialMarker` — the existing
glyph for "the real figure is larger than the number shown". This is the same disclosure
path §3.5 uses, at a different cause.

`Window7dLocalDays`'s comment explains why a floor and a window span that must agree
were made to derive from one constant. Any month floor, if one is ever added, belongs in
that same relationship rather than as a fourth independent number.

### 3.9 Not re-breaking PR #1071

PR #1071 (`Fix: Align abctl table headers with the values they name`, branch
`fix/header-align`) is open against `main` and fixes a defect this proposal could
reintroduce in three places. Its rule: **alignment is declared on the column and applied
to both halves from one field**, so a cell can never align itself in a way its heading
does not know about.

Three interactions, in descending order of how quietly they would fail:

**1. Silent — the sessions title is a lookup key.** Covered in §3.7. Renaming `SAVED` to
`SAVED ~` un-right-aligns the heading *and* makes the new alignment test skip the column.
`headerTitle` must learn to strip the marker.

**2. Silent — the band is the money surface #1071 does not cover.** Its ten changed files
are `events_columns.go`, `events_pane.go`, `sessions_pane.go`, `table_width.go` and six
test files. `spend_band.go` is not among them, and it left-flushes every figure with
`%-*s`. Today that is nearly invisible because the band holds one cost per span at most;
§3.3 puts four directly comparable costs in a row, which is exactly the condition #1071
exists for. Hence the uniform width and right alignment in §3.3. **The band should be
brought under the same rule, not given a parallel one.**

**3. Loud — fitted widths move.** #1071 pads headings against the **fitted** width, not
the declared one, because *"a title padded to a width the column no longer has is a title
bubbles truncates."* §3.6 changes every money cell's width, which changes what
`fitTableColumns` settles on, which changes the padding. This is handled automatically
since the padding is derived from the fitted width — but it means §3.6 cannot be reviewed
by diffing expected strings alone; the cell/heading agreement test is the check.

**Ordering.** This work overlaps #1071 in `sessions_pane.go` and `table_width.go`, and
commits 1 and 3 (§4) edit `headerTitle` — a function #1071 introduces. So the branch
**rebases onto #1071 after that merges** rather than being developed in parallel; there is
no version of it correct against today's `main` *and* against `main` once #1071 lands.

If #1071 stalls, commits 1–3 are the pieces to hold back. Everything from commit 4 onward
— the cents change, the scope-note deletion, the `month` window, the band — is independent
of it, which is the other reason the commit order puts the marker work first.

## 4. Staging

**One PR**, titled `Feat: Show the four budget spans in the abctl spend band`. Roughly
1,650 lines, measured rather than estimated: 135 test lines carry four-decimal dollar
literals, 25 non-test lines touch the money formatters, and `spend_band.go` is a 120-line
file getting a rewrite. Well inside the 10K ceiling.

It **rebases onto PR #1071** (§3.9). Two of the commits below edit `headerTitle`, a
function that PR introduces, so there is no ordering in which this work is correct against
today's `main` as well as post-merge `main`.

Reviewability comes from the commit sequence rather than from splitting the PR. Each
commit below builds, passes tests, and leaves the surface self-consistent — so the diff can
be read one concern at a time, and a contentious commit can be dropped without unpicking
the rest.

| # | Commit | § | ~lines |
|---|---|---|---|
| 1 | `fix(abctl): strip a column marker in headerTitle` | 3.7 | 15 |
| 2 | `test(abctl): assert every right-aligned column name resolves` | 5 | 30 |
| 3 | `fix(abctl): move the SAVED estimate marker to the column heading` | 3.7 | 60 |
| 4 | `fix(abctl): render money to cents, not ten-thousandths` | 3.6 | 300 |
| 5 | `fix(abctl): drop the misleading lifetime-totals note` | 1c | 40 |
| 6 | `feat(usage): add a month-to-date symbolic window` | 3.1 | 200 |
| 7 | `feat(costledger): retain 32 days so a 31-day month can be answered` | 3.8 | 100 |
| 8 | `refactor(abctl): make the spend polls a table of span chains` | 3.4 | 350 |
| 9 | `feat(abctl): show the four budget spans in the band` | 3.3 | 300 |
| 10 | `feat(abctl): cycle the four budget spans with w` | 3.2 | 150 |
| 11 | `fix(abctl): disclose a span the deployment cannot answer` | 3.5 | 120 |

Commit 2 lands before 3 deliberately: it is the assertion that would have caught commit
3's silent failure mode, and it belongs to #1071's design rather than to this work — so it
is also the one commit worth offering upstream on its own.

Commits 6 and 7 are the only ones touching `authlib`, and therefore the only ones that
cross the productization sync boundary. Keeping them contiguous and separately titled means
they can be lifted out into their own PR if that review needs to happen independently,
without rewriting the rest.

Commit 8 is the soft estimate. It generalises two hand-rolled poll chains — each with its
own snapshot, error, `reqSeq`, `tickGen`, message type and tick function across an 828-line
file — into five table-driven ones. It deletes duplication rather than adding it, but the
final shape is plan work, not proposal work.

## 5. Testing

- `StartOfLocalMonth` over the zone set `StartOfLocalDay`'s tests already use, plus a
  month whose 1st is a DST transition date, plus a December→January rollover.
- `ParseWindowSpec("month")` bounds, and that the label echoes.
- A `month` query spanning past retention reports partial rather than a short total.
- No-ledger degradation: `month` requested, ≤6h served, band renders `—` and not a
  figure. This is the assertion that would have caught 1a's class of defect.
- Band fitter: cells drop in the §3.3 priority, `TODAY` and `MONTH` last, nothing clipped
  at any width down to the drawer's floor.
- Four chains fail independently — one wedged span does not blank the other three.
- `w` cycles four spans from a zero-value `windowStep` starting at `1h` (the footgun
  `spendDrawerWindowDefault` existed to prevent).
- Money: the cents floor never prints `$0.00` for a non-zero charge, at every fitted
  column width.

Three that exist to protect PR #1071 (§3.9):

- **Every key in `sessionsRightAligned` resolves to a real column.** This is the gap that
  makes interaction 1 silent: `assertHeadersMatchCells` does
  `if !sessionsRightAligned[headerTitle(c)] { continue }`, and the `checked == 0` guard
  cannot fire while the other three numeric columns still match — so a renamed column is
  exempted rather than reported. Asserting the set against `sessionsColumns()` turns a
  future rename into a failure instead of a silence. Worth proposing to #1071 directly,
  independent of this work.
- **`headerTitle` round-trips a marked, padded title back to its bare name**, over the
  cross product of {marked, unmarked} × {padded, unpadded}.
- **The band's figures line up**, asserted the way `TestHeaderAlignment_SurvivesRendering`
  does it — slice the rendered lines and compare where the ink ends, not the pre-render
  strings, since that is what catches padding lost on the way to the screen.

## 6. Rejected alternatives

**A 4×4 span-by-metric matrix behind `$`.** The drawer's left column is a fixed
four-row tier panel with a documented no-growth rule (`spend_drawer.go:51-63`), so the
matrix would have had to displace the tier breakdown or add a mode toggle and a key. It
was also strictly less useful: totals per span only, because a per-model breakdown of
four spans is four grouped ledger queries. Extending `w` gives a full breakdown of *any*
span for no new key and no displaced content.

**Scoping the band to the drilled-in session.** Impossible for the headline: no session
dimension in the ledger, and `session=` with a symbolic window is a 400
(`sessionapi/usage.go:189`).

**Rewording `lifetime totals` instead of deleting it.** `per-session totals` was the
best candidate and still read as jargon against a band that no longer needs
contrasting. Deleting beats explaining once the band states its own spans.

**Keeping `15m` and `6h` in `w`.** Six entries to cycle to reach four useful ones. They
remain available through `abctl cost --window` and the Usage pane.

**A budget bar (`$703 / $2,000`, pace-to-month-end).** The genuinely interesting reading
of a month-to-date figure, and the natural follow-on — but it needs a configured budget
value, which does not exist anywhere yet. Deferred rather than rejected.

## 7. Decisions on the open questions

These were carried as open questions in the first draft. Each is resolved below, with the
escape hatch recorded in case the reasoning is wrong.

**7.1 The week stays rolling `7d`.** Not made calendar-aligned, and the inconsistency with
a calendar month is accepted. Three reasons. `snapshot.go:760-766` already records that
changing it alters what the window MEANS on the wire, that *"every client comparing
figures across the change would see a step"*, and that it is a product decision rather
than an implementation one — none of which this proposal improves on. The budget concern
is the **month**, and that one *is* calendar-aligned, so the span that has to match a
billing boundary does. And the labels already carry the distinction the way
`ParseWindowSpec` does: `7 DAYS` reads as a length, `TODAY` and `MONTH` read as
boundaries. *Escape hatch:* a `week` symbolic window is additive and needs only a
week-start convention; it does not block anything here.

**7.2 No feature flag.** The default-off rule lives in the `kagenti` repo's `CLAUDE.md`,
and its canonical mechanism is `rossoctl/backend/app/core/config.py` exposed through
`GET /api/v1/config/features` — a Python/React surface that does not exist in this repo.
This repo's own `CLAUDE.md` has no feature-flag rule. Beyond jurisdiction, the rule's
*intent* is already satisfied: PR 2 adds a window value nothing requests until PR 3, which
is off-by-default in the most literal sense available. And gating PRs 1 and 3 would mean
two live render paths through the band's fitting arithmetic, which is where essentially
every defect in this file has originated — `spendStripReservesRow`, `spendDrawerLines` and
`renderSpendBand` all carry comments about a reservation and a render disagreeing.
*Escape hatch:* if a gate is wanted, abctl already persists `Settings` (used for Usage
pane state), and that is where it goes — not a new mechanism.

**7.3 `CACHE HIT` comes off the band.** Option A was chosen over an option that kept the
volume metrics, so this follows the decision already made rather than reopening it. The
documented counter-argument is real — `spend.go:214-217` calls it *"the leading indicator
of the bill for an agent"* that *"moves before the dollar figure does"* — but the drawer's
tier column carries the same signal with strictly more information: `cache-read` against
`input` as proportional bars, which says how much the cache is saving and not merely that
it is being hit. It is one keypress away, and the band is cost-only in exchange.
*Escape hatch:* if it turns out to be missed, the cheap restoration is a fifth cell
labelled `CACHE 1H` at the right end, first in the drop order. The span must be in the
label — an unlabelled cache percentage beside four span-labelled costs is defect 1a
returning by the same door it left.
