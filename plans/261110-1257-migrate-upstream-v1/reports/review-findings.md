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

- **M1 (FailureMode) live cursor advances before enqueue** — ACCEPTED.
  Rationale: locked, fork-proven semantics (live observation = handled;
  required for busy-channel correctness); the loss window needs a
  non-queue enqueue failure after the advance (rare, logged at Error);
  the common shutdown case is protected by H3's fix (record kept + resume
  requeue). Changing it would re-litigate the Phase 7 spec.
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
- L2 FLOOD_WAIT > 15 min misclassified as stall — ACCEPTED (retryable;
  classification-only).
- L3 Win32-hostile dir segments (.. , trailing dots) accepted by sanitizer,
  no escape (verified empirically) — ACCEPTED (availability/UX only).
- L4 unbounded watch fan-out at boot — ACCEPTED (single-user deployments;
  budget-bounded per watch).
- L5 pico runs as root — FIXED: numeric USER in Dockerfile.pico.
- L6 fake double-append inflates Fetched against the unpaginated fake —
  FIXED with M7.
- L7 ext.Context seed collision (same-tick copies) — ACCEPTED (tiny
  Windows-nanotime window; per-copy rand kept for goroutine safety).
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
