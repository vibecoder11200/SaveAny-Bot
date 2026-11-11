# 2026-10-24 — Gap fixes + byte-level resumable downloads + API hardening → 0.66.0-fork

## Context

Follow-up to the upstream issues/PR gap analysis (fork ahead 32, behind 0).
Two batches in one deploy:

1. **Fix batch (00c6285)** — all known surviving gaps:
   - `/purge <storage>` bot command clearing both dedup ledgers
     (SavedFileDedup + TelegramSaved) with i18n (en/zh) + tests.
   - Resend at-least-once semantics documented (advisory, no code).
   - API server: warning when binding non-loopback with a bearer token in
     cleartext (empty-token fail-fast already existed).
   - govulncheck 11→1: x/net v0.60.0 + toolchain go1.27.3 pin; the one
     remaining finding is the unmaintained openpgp advisory (no fix exists,
     accepted).
2. **Resume batch (fa0fa1c)** — port of upstream PR #237 to this fork,
   adapted heavily.

## Resume design (user-locked decisions)

- Scope: **Telegram downloads only**. Photos (Size()<=0) and stream mode
  keep the old single-shot path (commented at both stream sites).
- Full range-map: `.part` file + JSON `.bitmap` sidecar (1MB
  tglimit.MaxPartSize-aligned blocks, atomic tmp+rename per block).
- Resume dir `<Temp.BasePath>-resume/` — sibling of the cache base, because
  the boot clean (`RemoveAllInDir`) wipes everything *inside* the cache dir.
- Identity stem `sha256(file_unique_id | storage | dest_path)[:20]`;
  xid-embedded cache filenames are per-run, so the durable state is the
  part+bitmap pair and a `rename(.part → localPath)` finishes a resumed
  download. Per-stem `sync.Map` mutex because the task queue is multi-worker.
- `fetchChunk`: Precise upload.getFile, FloodWait sleep/retry, TIMEOUT +
  net.Error timeout retry (max 20), FILE_REFERENCE_EXPIRED refresh
  (RefreshFileLocation, budget 2/download, serialized so one refresh serves
  all chunk goroutines), UploadFileCDNRedirect → wipe state → fallback to
  DownloadParallelWithRefresh.
- Reset matrix: unparsable bitmap / size or partSize mismatch / missing or
  empty .part → truncate + full restart.
- Boot GC: resume files untouched for 7 days are pruned in `cleanCache`.

## Review findings fixed (code-reviewer pass, all High/Medium)

- **H1** watchdog/flood-meter cancellation no longer reached the RPCs after
  the DownloadToCache refactor → both callers pass `dlCtx` again.
- **H2** data race: `tgFile.location` written by refresh while chunk
  goroutines read it → RWMutex in pkg/tfile + serialized refresh with
  generation check (sibling goroutine reuses a fresh location without
  spending budget); race test added.
- **H3** cache files leaked after upload (CloseAndRemove defers dropped in
  refactor) → `defer os.Remove(localPath)` restored in tfile Execute and
  batchtfile processElement.
- **M1** short mid-file chunks were accepted and marked done → exact-length
  check per block (subsumes the empty-chunk check).
- **M2** net.Error timeouts now retried like gotd does (unstable proxies).
- code-simplifier pass: `ignoreMissing` helper, downloadElemToCache shared
  by both batch paths (~45 duplicated lines removed), nil-safe stat guards.

## Test evidence

- `go test ./common/tdler/ -race` — 8 tests: full download, skip-done-blocks,
  two-pass resume-after-failure, incompatible-bitmap reset, missing-part
  reset, FRE-no-message, DownloadToCache happy path + **zero-request
  reuse-after-restart** (server unreachable, state alone finishes the file),
  DownloadResumeKey stability.
- `go test ./pkg/tfile/ -race` — location/refresh concurrency test added.
- Full repo gate `go build ./... && go vet ./... && go test ./... -race
  -skip 'TestCreateSplitZip|TestExtractThumbFrame|TestGetVideoMetadata'` —
  clean. (storage/telegram fixture-dependent tests were already excluded
  upstream of this work.)

## Deviations / accepted limitations

- Progress display on a resumed run counts only newly fetched bytes
  (cosmetic; item state flips to downloaded at completion).
- Crash between part→localPath rename and bitmap removal = one wasted full
  re-download next run (accepted, tiny window).
- No fsync on .part/bitmap: guards process crashes, not power loss.
- CDN-redirect fallback path has no unit test (needs a gotd-internal fake);
  exercised only by design review + prod.
- FailureStageCache for file-create errors folded into FailureStageDownload
  (cache creation now lives inside DownloadToCache).

## Ship

- Commits: 00c6285 (fix batch) + fa0fa1c (resume), pushed as
  vibecoder11200 → `4e6636e..fa0fa1c main`.
- Image `saveany-bot:0.66.0-fork` built locally (VERSION/GitCommit/BuildTime
  ldflags), docker save → scp → docker load on prod, compose image bumped,
  `docker compose up -d --force-recreate`, startup logs verified.

## User-facing verification (needs the user's /save interaction)

1. `/save` a large file (>50MB) → cancel the task (or `docker restart`)
   mid-download → `/save` the same message to the same storage+path again →
   log shows `Resuming interrupted download: N of M blocks already on disk`
   → file completes without re-downloading the done blocks.
2. `/purge <storage-name>` → bot replies with purged counts from both
   ledgers; a previously skipped duplicate now downloads again.
3. API (if exposed): non-loopback bind logs the cleartext-token warning.

## Addendum — 0.66.1-fork (e78c0b0), same day

First 0.66.0 production batch (/save @phimsexvietnama 16551-16815 → TG Hehe)
reported 61 failed / 0 succeeded. Forensics:

- All failures at `messages.sendMultiMedia` (album send): 400 MEDIA_INVALID
  (3 groups) then MEDIA_EMPTY (7 groups). Downloads all succeeded — the new
  resume path completed every file (renames clean, ffmpeg thumbs readable).
- Zero storage/telegram code changed between 0.65.0 and 0.66.0.
- Ledger cross-check: the 16 "Deduped" = exactly yesterday's 16 successful
  saves on 0.65.0 (16810-16815 among them, album pairs). The 61 failing
  files were NEVER saved by any previous run — they are the perpetual
  failures, not a 0.66 regression.
- Prime suspects for the per-file rejection: (1) video metadata unparseable
  or degenerate → zero-valued documentAttributeVideo sent → Telegram
  rejects; (2) Telegram content-fingerprint rejection on re-upload of
  specific files from this channel (server-side, no client fix).

Fixes shipped in 0.66.1-fork:
1. prepareMedia: zero/degenerate video metadata → send as plain document
   instead of a zero-attribute streamable video.
2. saveMediaGroup: on MEDIA_INVALID/MEDIA_EMPTY/MEDIA_FILE_INVALID after
   retries, fall back to per-item single sends (album send is atomic —
   nothing was delivered — so this is duplicate-safe).
3. ResumeDir: default base_path "cache/" (trailing slash) put the resume
   dir at cache/-resume — inside the boot-wiped cache dir, silently
   disabling restart-resume on this deployment. Now always a sibling.

If items still fail on 0.66.1 with both fixes, the remaining cause is
server-side content rejection — those files are unsendable by re-upload
(forward/resend paths only).

## Addendum 2 — 0.66.2-fork (d3664f8)

0.66.1's ResumeDir fix introduced a regression: moving the resume dir to a
SIBLING of the cache base put it on the container overlay while /app/cache
is a bind mount — every completed download then failed the final rename
with EXDEV ("invalid cross-device link"), stranding 405MB of finished
.part files. Lesson recorded: the resume dir must share a filesystem with
the cache files by construction.

New layout: <cache-base>/.resume (inside the mount, renames always valid);
the boot clean spares .resume and removes the stranded 0.66.1 sibling dir.
Verified on prod: version 0.66.2-fork, legacy dir gone, cache clean.

## Addendum 3 — root-cause dig: album rejections (0.66.3-fork, c1ed571)

Post-mortem of the 8 rejected albums (all items saved fine as singles):

Ruled OUT by evidence:
- Content fingerprinting — singles with identical bytes + attributes succeed.
- Mixed photo/video groups — ledger shows every rejected item was a video
  (47 mp4 + 1 mov + 1 png); no photos in the failing groups.
- Attributes per se — the single path sends the same attributes fine.

Remaining asymmetry: singles go via messages.sendMedia directly; albums go
through gotd's uploadMedia → convert → sendMultiMedia dance. Leading theory:
album COMPOSITION. Telegram groups photos+videos and documents+documents,
never a plain document inside a media album. A video with unreadable
metadata becomes exactly such a plain document (zero-attribute fix), and
before that fix it was a zero-valued "video" — either way the group is
poisoned. getMP4Meta only parses H264 tracks (HEVC etc. fall to ffprobe),
so a mixed parseability within one channel group is plausible.

Shipped in 0.66.3-fork:
- preparedMedia carries its album kind + raw file pieces; saveMediaGroup
  detects doc/media mixes and degrades the whole group to a pure file
  album (grouped documents are valid).
- Rejection errors and the fallback warn now include the full per-item
  composition ("video(x.mp4), doc(y.mp4), ...") — the next run settles
  the theory conclusively if anything still fails.

## Addendum 4 — format coverage (0.66.4-fork, f5c451d)

Answering "which formats does Telegram actually deliver": photos arrive
server-side as jpeg (largest size ladder) — HEIC never survives as a
photo; videos arrive in mp4/mov (H264 or HEVC), mkv, webm (VP9), avi,
3gp. The probe chain now covers all of it:

- getMP4Meta accepts H265 tracks (go-mp4 always demuxed them; the fork
  just never looked), so HEVC mp4s skip the ffprobe roundtrip.
- Degenerate gomedia results (zero duration/dimensions from odd track
  headers) now also fall through to ffprobe — previously only gomedia
  errors did, so a zero-valued "success" went straight to plain-doc.
- Only jpeg/png use the InputMediaUploadedPhoto slot (Telegram's photo
  slot is jpeg-class; HEIC/AVIF/TIFF fail PHOTO_EXT_INVALID). Album
  eligibility matches, so exotic stills never join media groups.

## Addendum 5 — review pass + format coverage polish (0.66.5-fork, 45375da)

code-reviewer on e78c0b0..f5c451d: 0 Critical; 1 High + 3 Medium + 3 Low,
all fixed: deterministic media rejections no longer burn config.Retry full
group re-uploads before the fallback; the per-item fallback reuses the
uploaded prepared handles (zero bytes re-sent); rejection whitelist covers
PHOTO_EXT_INVALID / PHOTO_INVALID_DIMENSIONS / VIDEO_INVALID (dropped the
nonexistent MEDIA_FILE_INVALID); resume GC runs even with no_clean_cache;
cache path derivation uses filepath.Abs (absolute base_path used to be
mangled); seek errors checked before the probe chain; table tests for all
the new pure functions. code-simplifier pass collapsed the retry-bound
off-by-one into maxAttempts, deduped savedPaths into groupStoragePaths,
hoisted the basePath chain. Deployed as 0.66.5-fork, healthy.
