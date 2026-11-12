# Phase 11 review findings ledger

Reviewers: Security Adversary, Failure Mode Analyst, Assumption Destroyer,
Scope/Complexity — all over `git diff upstream/main..v1` (186 files,
+13,816/−454). Findings deduped across lenses; one owner each.

Severity scale: Critical (blocker) / High (fix before merge) / Medium (fix
if cheap, else accept with rationale) / Low (logged).

## Critical

None found by any lens.

## High — all FIXED

- **H1 (FailureMode) watch_catchup.go post-flush advance skips failed
  singles** — the cursor advance was guarded only against failed ALBUM
  flushes; a single whose resolve/create/AddTask failed still had the
  cursor jump past it → silent file loss on the next boot.
  **Fix**: resolution counters — `singlesFed` (intake) vs `singles`
  (resolved outcomes incl. dedup/skip) for the singles path,
  `albumResolved` (per-file resolved incl. filtered/skipped groups) for the
  album path; post-flush advance only when both paths fully resolved.
  Commit: `fix(watch): catch-up cursor advances only past resolved messages`.
- **H2 (Assumption) getHistory pagination window** — MinID is applied
  server-side AFTER slicing; with `AddOffset:-100` the anchor can ride in
  the window and MinID strips it, making every page read "short" and
  stalling the walk after page 1 (~99 msgs/boot instead of the 500 cap).
  **Fix**: drop MinID from fetchHistoryPage (planPagesWithSize already
  filters `msg.ID <= cursor` client-side — correct under both window
  interpretations); e2e fake taught real offset math so the class of bug
  can never hide again (M7). Commit: `fix(watch): catch-up pagination`.
- **H3 (Assumption) dead queue-closed guard** — `queue.Add` returned a
  fresh `errors.New("queue is closed")` while the fork's record-preservation
  check used `errors.Is(err, queue.ErrQueueClosed)` — never matched, so the
  shutdown-loss protection the comment claims was dead code.
  **Fix**: `queue.Add` wraps the sentinel. Commit: `fix(core): queue
  closed sentinel`.
- **H4 (Scope) batch resend records no dedup row** — single-file resend
  recorded the dedup ledger; the batch path did not → batch-resend files
  invisible to /history and re-save dedup.
  **Fix**: batch tryResendElem records on success, mirroring the single
  path. Commit: `fix(batch): resend records dedup ledger`.

## Medium

- **M1 (FailureMode) live cursor advances before enqueue** — FIXED
  (ak-fix round, user-directed): the live advance is gated on per-event
  RESOLUTION — enqueued, dedup-skipped, or deterministically filtered
  (mediatype/user-scope rejects, no-default-storage skips all count as
  resolved, preserving busy-channel correctness). An enqueue failure or a
  user-fetch error leaves the message below the cursor and the next boot's
  catch-up re-fetches it. Buffered album members resolve at flush (which
  already advanced per flushed group) — a failed album flush now stays
  re-fetchable too, which the old pre-advance semantics lost forever.
- **M2 (FailureMode+Assumption) live×catch-up TOCTOU on singles** — FIXED:
  creation-time file-id dedup added to processWatchSingle (same layer the
  album path already applies; fail-open). Residual window (hash-less
  sources racing) accepted — same exposure as the fork v0.
- **M3 (FailureMode) ErrSkippedLarge not special-cased in the
  saveMediaGroup retry closure** — FIXED: sentinel short-circuits like the
  task-level closures; oversized members skip instead of poisoning the
  whole batch.
- **M4 (FailureMode) album MEDIA_* fallback partial delivery reported as
  total failure** — FIXED: delivered members get their dedup/file-id
  records before the error returns, so retry cannot duplicate them.
- **M5 (FailureMode) importer probe errors conflated with absent tables** —
  FIXED: probe errors AND missing core tables (saved_file_dedups,
  telegram_saved_files, pending_tasks: present in every v0 fork release)
  abort the whole import; PRAGMA table_info returns empty rows, not an
  error, for a dropped table, so absence is checked explicitly.
- **M6 (Assumption) CreateTaskRecord error swallowed in AddTask** — FIXED:
  record failures fail the enqueue (a task must not run without its record;
  the watch cursor's durability boundary depends on it).
- **M7 (Assumption) e2e fake ignores pagination params** — FIXED with H2:
  fake honors OffsetID/AddOffset/MinID/Limit.
- **M8 (Scope) resend/dedup orchestration duplicated across the two task
  executors** — DEFERRED to the simplifier pass (its consolidation brief).
- **M9 (Scope) guard wiring duplicated 4x; stream paths inline the error
  chain instead of dlutil.DownloadFailure** — DEFERRED to simplifier.
- **M10 (Scope) settings→handlers layering inversion** — DEFERRED to
  simplifier (move thin validators to a neutral package).
- **M11 (Scope) boot vs settings duplicate v0-smell heuristics** — FIXED:
  one detection function in database/ used by both.
- **M12 (Scope) pkg/tfile SanitizeFileName re-implements
  fsutil.SafeBaseName** — DEFERRED to simplifier.
- **M13 (Scope) fork tests call database.Init (Fatalf) instead of
  skip-on-driver-failure** — FIXED: probe-open + skipf like
  database/openTestDB.

## Low — logged (fixed when trivial)

- L1 stream task missing MarkTaskElementDone (crash between upload and
  record update re-runs the task) — FIXED.
- L2 FLOOD_WAIT > 15 min misclassified as stall — FIXED (ak-fix round):
  DownloadFailure checks the meter BEFORE the watchdog (specific beats
  generic), and the floodwait middleware now caps auto-sleep at
  MaxAutoFloodWait (stall window - 1m): longer waits are refused up front
  with the true cause instead of being parked past the watchdog and
  killed as "stalled". Waits up to the cap still sleep and resume
  transparently.
- L3 Win32-hostile dir segments — FIXED (ak-fix round): SanitizeRelDirPath
  trims trailing dots/spaces per segment (Windows resolves "foo." to
  "foo" on disk, diverging the stored path) and shifts reserved device
  stems ("con" to "con_"), the same rules pkg/tfile applies to filenames;
  ".." was already dropped.
- L4 unbounded watch fan-out at boot — FIXED (ak-fix round): the
  sequential per-watch loop now enforces boot-run ceilings (500 files
  total across watches, 30 min wall clock), checked between watches;
  unvisited watches keep their cursors and defer to the next boot.
- L5 pico runs as root — FIXED: numeric USER in Dockerfile.pico.
- L6 fake double-append inflates Fetched against the unpaginated fake —
  FIXED with M7.
- L7 ext.Context seed collision — FIXED (ak-fix round, upstream-external
  constraint honored): mygotg seeds per-copy rand from the wall clock and
  the seed feeds Telegram RandomID send nonces, so same-tick copies that
  both send collide and the second send is silently dropped. newExtContext
  serializes copies and waits for the clock to advance past the previous
  copy; both creation sites (middleware child, watch notify) route through
  it.
- L8 importer storageIDs case-collision last-wins — FIXED: warn on
  case-colliding names.
- L9 Windows reserved device names (CON, NUL…) unhandled by
  SanitizeFileName — FIXED: reserved-name prefix.
- L10 resolveWatchTarget dead override branch — QUEUED (simplifier pass).
- L11 openReadOnlyDialector dead abstraction — QUEUED (simplifier pass).
- L12 README_zh lacks the fork banner — FIXED.
- L13 plans/ process artifacts in the repo — ACCEPTED (plan folder is the
  coordination surface; pruning is a user choice).

## Clean areas (lens-confirmed)

Dedup fail-open everywhere; resend ambiguity timing (no ledger row before
confirmation); importer atomicity (marker in-tx, hollow refusal, read-only
source); watch index migration on every DB shape; watchdog/meter wiring on
all four download paths; progress-edit queue final-edit survival; SkipLarge
classification end-to-end; permission/owner gates first-statement fail-closed;
regex bounded (RE2 + 512 cap + bounded cache); importer SQL parameterized;
no secrets in logs; i18n escaping on all fork call sites; mygotg context
copy + PeerStorage/Sender sharing; queue position stability; Windows
file-URI + path handling; no dead v0 ported code; no second floodwait
waiter; all 7 locked feature groups present.

## Failure-mode re-review of the fix commits (38d3e2e..652718e)

Verdict: 0 Critical, 0 High. Notes (accepted, documented):

- **DB pool serialized to 1 conn** (H-fix batch): a transaction that
  queried the GLOBAL handle while holding its tx would deadlock. No such
  call site exists (importer reads the v0 file through its own separate
  handle; TouchWatchCursor is fully tx-scoped; Init-time helpers are
  single-threaded). Any future tx + global-DB mixup must be avoided.
- **H2 short-page rule vs deleted-message gaps**: a page shorter than the
  window because of deleted messages stops the walk one boot early; the
  per-single cursor touches make it converge over subsequent boots
  (pre-existing planner semantics, not a H2 regression).
- **M5 refuses v0 files missing a core ledger table** (e.g. saved_file_dedups
  from a hypothetical pre-dedup fork state): conservative by design — the
  production v0 schema has all three tables (fixture DDL is the reference).
- **M4 storage-level SkipLarge inside the album fallback**: if a caller
  bypasses the batch task's size pre-filter, a skipped member is logged at
  the storage layer while the task reports overall success. The batch task
  pre-filter (batchtfile execute.go) prevents this on all real paths.

## ak-fix round (user-directed: fix all remaining issues)

Scope: every ACCEPTED finding that was a real defect and fixable without
reversing a locked product decision. M1, L2, L3, L4, L7 fixed as above.
Still accepted, NOT defects or user-reserved: L13 (plans/ artifacts are the
coordination surface; pruning stays a user choice), L2's residual
double-wait edge (inherent to watchdog-vs-waiter construction, both labels
now honest), the gofmt drift in untouched upstream files (app/, cmd/:
formatting them would add pure-noise hunks to the fork diff),
go-enum regeneration provenance headers (tool stamps its own build info;
generated code byte-identical, artifacts committed), ncruces Windows
absolute-path defect (upstream, skipf documented), the two locked resume
regressions (plan decision: upstream native resume wins).
