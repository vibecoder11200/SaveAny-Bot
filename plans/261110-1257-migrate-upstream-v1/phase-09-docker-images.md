---
title: "Phase 9: Docker Images (micro / pico)"
status: todo
phase: 9
priority: P2
effort: "4h"
dependencies: [phase-01-start]
---

# Phase 9: Docker Images (micro / pico)

## Overview

Recreate `Dockerfile.micro` and `Dockerfile.pico` adapted to the v1 build (CGO-free, tags adjusted for what v1 still supports), and verify the user's MinIO works through upstream's built-in `s3` storage type.

## Key Insights

- v1 main Dockerfile is a 3-stage build: node:26-alpine web stage → CGO_ENABLED=0 go build with `--from=web /dist` copied in → runtime with curl/ffmpeg/yt-dlp (`Dockerfile:1-46`). The Go build itself compiles with an EMPTY committed dist (`web/dist/.gitkeep`, `web/embed.go:5`) — micro/pico can skip the node stage entirely; the web console is simply absent (API serves; UI route empty).
- Build tags verified in v1: `no_jsparser` EXISTS (`parsers/js/js.go:1` `//go:build !no_jsparser`, stub at `parsers/js/js_stub.go:1`) and additionally `no_playwright` exists — both usable for image slimming. `sqlite_glebarez` exists (`database/driver_glebarez.go:1`). `no_minio` does NOT exist (no minio package in v1) — the tag must be dropped.
- Fork's images (researcher-02 §9): micro = alpine + curl runtime, `-tags=no_jsparser,no_minio`; pico = `FROM scratch` + ca-certificates + `SSL_CERT_FILE`, `-tags=no_jsparser,no_minio,sqlite_glebarez` (glebarez drops the ncruces WASM machinery for the smallest static binary). The fork never modified them; they are carried v0 artifacts — recreation is adaptation, not invention.
- MinIO: v1 `s3` config is endpoint/access/secret/bucket/base_path/region/virtual_host (`config/storage/s3.go:9-43`); MinIO generally needs path-style addressing (`virtual_host = false`) unless behind a virtual-host-capable proxy. Upstream auto-migrates legacy `type=minio` rows to `s3` at boot (`database/storage_record.go:40-70` via `boot/boot.go:41-43`), normalizing endpoint scheme — user requirement satisfied by configuration, not code.
- pico/micro lack ffmpeg/yt-dlp, and today the degradation surfaces as opaque mid-task failures (`storage/telegram/video_split.go:177-201` invokes ffmpeg with no presence check). The fork surfaces it at configuration time instead, mirroring the rclone pattern (`storage/rclone/rclone.go:40` runs `exec.LookPath` at init): Telegram storage `Test` (or `Init`) warns when split-capable config is on and ffmpeg is absent; ytdlp parser creation warns when yt-dlp is absent.
- No Docker is assumed available on this Windows box: image BUILDS are specified exactly but executed where Docker exists; everything verifiable locally (tag combinations, compile) is verified here.

## Requirements

**Functional**
- `Dockerfile.micro`: golang:1.27-alpine builder, `CGO_ENABLED=0`, `-tags=no_jsparser,no_playwright`, no web stage; alpine runtime with ca-certificates + curl (healthcheck-able); non-root user; binary + entrypoint.
- `Dockerfile.pico`: same builder with `-tags=no_jsparser,no_playwright,sqlite_glebarez`; `FROM scratch` + `ca-certificates.crt` + `SSL_CERT_FILE` env; static binary entrypoint.
- Both: VOLUME/expose notes for the data dir (`/data`), docs comment stating the web console is not bundled (empty dist embed) and that ffmpeg/yt-dlp are unavailable (telegram video-split and ytdlp/parser features degraded accordingly).
- NO new CI workflow for Docker images (locked decision): this phase ships `Dockerfile.micro`/`Dockerfile.pico` only; CI image building is out of scope and not locally checkable (no Docker on this Windows box) — Phase 10's merge proposal mentions it as a possible post-validation follow-up in one line.
- Degraded-tooling warnings (mirroring the rclone `exec.LookPath` pattern, `storage/rclone/rclone.go:40`): Telegram storage `Test` (or `Init`) emits a warning when split-capable config is on and ffmpeg is absent; ytdlp parser creation warns when yt-dlp is absent — so pico/micro users see the degradation at config time rather than as opaque mid-task failures (`video_split.go:177-201`).
- MinIO verification: documented `s3` storage config for the user's MinIO (endpoint, path-style), plus a runtime verification procedure; any code change only if a real incompatibility surfaces.

**Non-functional**
- Images must build from the v1 branch with no extra steps beyond `docker build -f Dockerfile.micro .`.
- Local proxy for correctness: the exact tag combos must compile on Windows (`go build -tags ...`) — no Docker needed for that half.

## Architecture/Approach

- Copy the fork image structure, adapt the toolchain line: drop `no_minio`, keep `no_jsparser`, add `no_playwright` (new in v1, free size win), keep `sqlite_glebarez` on pico only (ncruces default is fine on alpine-glibc... actually alpine is musl — irrelevant, both drivers are pure Go; glebarez stays pico-only for size). Rejected: building the web UI into micro — defeats the image's purpose; the console is optional.
- MinIO via `s3` config only. Rejected: any minio-specific code — locked decision; upstream migration handles legacy rows.

## Related Code Files

| Action | Path (v1 branch) | Notes |
|--------|------------------|-------|
| Create | `Dockerfile.micro` | Adapted from fork v0 artifact |
| Create | `Dockerfile.pico` | Adapted; scratch + glebarez |
| Modify | `storage/telegram/telegram.go` | ffmpeg-absence warning in `Test`/`Init` when split-capable config is on (rclone `exec.LookPath` pattern) |
| Modify | ytdlp parser creation path (locate exact file in step 6) | Warn when the yt-dlp binary is absent at parser creation |
| Modify | `docs/` or `README.md` (placeholder; full docs Phase 10) | Image usage notes |

## Implementation Steps

1. Verify tag combos compile locally: `go build -tags "no_jsparser,no_playwright" ./...` and `go build -tags "no_jsparser,no_playwright,sqlite_glebarez" ./...` (both CGO-free; Windows-native proof).
2. Write `Dockerfile.micro`: builder stage `golang:1.27-alpine` (git + ca-certificates for module fetch), copy go.mod/go.sum then source, build with the tag combo and `-trimpath`; runtime `alpine:3`, install ca-certificates + curl, create non-root user, copy binary, `VOLUME /data`, entrypoint with `--data-dir /data`.
3. Write `Dockerfile.pico`: same builder (plus tag), runtime `FROM scratch`, `COPY --from=builder /etc/ssl/certs/ca-certificates.crt`, `ENV SSL_CERT_FILE=...`, copy static binary, same entrypoint/VOLUME.
4. Where Docker is available: `docker build -f Dockerfile.micro -t saveany-fork:micro .` and pico equivalent; `docker run --rm saveany-fork:pico --help` smoke test (binary runs, prints help, exits).
5. MinIO: write the recommended `s3` storage JSON (endpoint with scheme, `virtual_host: false`, bucket, base_path) into the Phase 10 migration doc draft; runtime verification is user-executed (their MinIO credentials): create storage via web UI/API, test-connection button / `POST /api/v1/storages/test`, save a file, confirm object in bucket.
6. Presence warnings (rclone pattern): add the ffmpeg-absent warning to Telegram storage `Test` (or `Init`), emitted only when split-capable config is on; add the yt-dlp-absent warning at ytdlp parser creation (locate the exact parser file first). No CI workflow is created (locked decision — Phase 10's merge proposal carries the one-line follow-up note).

**Builder stage sketch (shared shape, both images):**
`FROM golang:1.27-alpine AS builder` → `apk add git ca-certificates` → `COPY go.mod go.sum ./` + `RUN go mod download` → `COPY . .` → `RUN CGO_ENABLED=0 go build -trimpath -tags "<combo>" -o /out/saveany-bot .`. Runtime stages differ only in base, packages, and user handling as in steps 2–3.

## Todo

- [ ] Both tag combos compile locally (Windows, CGO-free)
- [ ] Dockerfile.micro written and (where Docker exists) built
- [ ] Dockerfile.pico written, built, scratch smoke-test passes
- [ ] Degraded-feature notes (web console absent; ffmpeg/yt-dlp absent) in image headers
- [ ] ffmpeg/yt-dlp absence warnings at config time (Telegram `Test`/`Init` + ytdlp parser creation)
- [ ] Sizes recorded in the commit message: image sizes where Docker exists, ELSE built binary sizes compared across the two tag combos (locally achievable)
- [ ] MinIO s3 config documented + user runtime verification procedure
- [ ] Confirmed NO CI workflow created (locked decision; follow-up note lives in Phase 10's merge proposal)

## Verification

```bash
go build -tags "no_jsparser,no_playwright" ./...
go build -tags "no_jsparser,no_playwright,sqlite_glebarez" ./...
go vet ./...
# where Docker exists:
docker build -f Dockerfile.micro -t saveany-fork:micro . && docker run --rm saveany-fork:micro --help
docker build -f Dockerfile.pico  -t saveany-fork:pico  . && docker run --rm saveany-fork:pico  --help
```

## Risk Assessment

1. **Scratch image TLS/timezone gaps** (low likelihood, medium impact): pico needs ca-certificates (done) and TLS time validation relies on system time — fork's v0 pico proved this works. Mitigation: keep `SSL_CERT_FILE` env exactly as the v0 artifact had it.
2. **Size regression without a JS-parser exclusion** — moot: `no_jsparser` verified present in v1; if moejs later hard-codes a dependency, image grows; document measured image sizes in the commit for tracking.
3. **MinIO virtual-host mismatch** (medium likelihood, low impact): wrong `virtual_host` shows as signature errors at test time. Mitigation: documented default false + test procedure catches it in minutes.

## Security Considerations

- Non-root user in micro; pico is rootless by construction (scratch has no user table).
- No secrets baked into images: storage configs live in the DB under `/data` (volume), never in layers.
- Healthcheck via curl in micro only (pico has no shell — document liveness as process-exit).

## Next Steps

Phase 10 folds the image docs + MinIO procedure into the migration guide and runs the full matrix.
