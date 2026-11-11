# Phase 6 — Watchdog v2: phát hiện download "bò" do flood-wait (ưu tiên cao — pain thực tế 2026-10-12)

> **Red-team 2026-10-12 (findings #1 + Scope #3) — thiết kế cũ bị bác bỏ.** Byte-floor 4MiB/15ph sai toán 4-5 lần: downloader chạy N thread song song (90MB → 4 thread theo `BestThreads`, `common/utils/dlutil/dl.go:8-25`), mỗi thread tự ngủ FLOOD_WAIT riêng mỗi part 1MiB (`tglimit.MaxPartSize`) → crawl cấu trúc tối thiểu đã ~4MiB/15ph, sự cố thật ~20MiB/15ph = gấp 5 lần floor → **không bao giờ bắn**. Link chậm hợp lệ ~20-25KB/s cũng không phân biệt được bằng byte với crawl 23KB/s. Thiết kế mới đo đúng tín hiệu: **thời gian RPC bị flood-wait** — tín hiệu mà link khỏe không bao giờ tạo ra.

Sở hữu file: `common/utils/dlutil/floodwait_meter.go` *(mới)* + `floodwait_meter_test.go` *(mới)*, `common/tdler/dler.go` *(hook Observe vào eofAwareClient + thêm NewDownloaderWithMeter)*, `core/tasks/tfile/execute.go` + `stream.go` + `core/tasks/batchtfile/execute.go` *(khởi tạo meter song song watchdog v1 — KHÔNG đụng writer.go/callback Kick của v1)*. Không phụ thuộc phase nào — làm được ngay, song song mọi phase.

## Bối cảnh (bằng chứng prod 2026-10-12)
File 90MB bò 66 phút (09:13→10:19, ~20MB/15phút ≈ 23KB/s) vì mỗi chunk dính FLOOD_WAIT ≤5 phút mà floodwait middleware cố ý chấp nhận (tối đa 3 lần × 5 phút mỗi RPC, `client/middleware/default.go`). Bytes vẫn rỉ nên watchdog v1 (im lặng tuyệt đối) đứng im đúng thiết kế. Kết cục `engine was closed`, mất file. Người dùng đã chốt: **23KB/s là kẹt** — kỳ vọng mọi download duy trì được nhanh hơn mức đó đáng kể.

## Thiết kế
**Hai rule độc lập, hai thông điệp lỗi riêng biệt** (bổ sung, không thay thế v1):

1. **v1 — giữ nguyên StallWatchdog đã ship**: im lặng tuyệt đối 15 phút → `download stalled: no data received for 15m`. Không đổi một dòng logic; đảm bảo chết-im bị cắt đúng 15 phút sau byte cuối, giữ Kick(n)/writer callback như hiện tại.
2. **v2 — FloodWaitMeter (mới)**: tích lũy thời gian của các RPC "chậm bất thường" trong cửa sổ 15 phút; vượt ngưỡng → cancel download, lỗi: `download crawling: flood-wait absorbed Xs of last 15m (account rate-limited)`.

**Vì sao đo thời gian chờ thay vì byte** (finding #1):
- Byte-floor cấu trúc không thể đúng: N thread × 1MiB/part với mỗi thread tự hấp thụ flood-wait → tổng byte của crawl luôn ≥ floor khi N ≥ 1 phần/part-cycle.
- Link chậm thật không tạo FLOOD_WAIT — thời gian chờ là tín hiệu phân biệt duy nhất giữa "tài khoản bị rate-limit" và "đường truyền chậm". Healthy link: RPC ~48ms-vài giây.

**Cơ chế**:
- `dlutil.NewFloodWaitMeter(parent context.Context, cancel context.CancelFunc)` — goroutine window 15 phút; `Observe(dur time.Duration, err error)`: chỉ tính RPC **thành công** (err==nil) có `dur ≥ slowRPCThreshold`; cộng dồn atomic `slowNanos`. Cuối window: `slowNanos ≥ maxSlowNanos` → `cancel()` + `Crawled() = true`; reset về 0 mỗi window (mỗi window độc lập như v1). `Close()` dừng goroutine.
- Hằng số (không đưa ra config — KISS): `meterWindow = 15 * time.Minute`, `slowRPCThreshold = 2 * time.Minute`, `maxSlowNanos = meterWindow / 2` (7.5 phút).
- **Hiệu chuẩn & biên lành minh (ghi vào doc comment)**: part 1MiB phải về trong <2 phút mỗi RPC. Link 4-thread chia sẻ cần duy trì >~35KB/s tổng để mọi RPC dưới ngưỡng — đúng vùng người dùng đã chốt là "chậm hơn thế = kẹt". Sự cố thật (sleep 5phút/RPC × 4 thread) tích lũy ~60 phút chờ mỗi window 15 phút → bị cắt ở tick đầu tiên. Chuỗi flood-wait ngắn (<2 phút/lần) không tính — nhưng khi đó throughput ≥ 4MiB/phút ≠ crawl, đúng hành vi mong muốn.
- **Hook điểm duy nhất**: `eofAwareClient.UploadGetFile` (`common/tdler/dler.go:30`) đã bọc mọi UploadGetFile của mọi thread — thêm `time.Since` quanh call + `meter.Observe(dur, err)`. `NewDownloader(file)` giữ nguyên chữ ký (meter nil = không đo, các caller khác không đổi); thêm `NewDownloaderWithMeter(file, meter)`.

## Bước thực hiện
1. Viết `floodwait_meter.go` (atomic accumulator + window goroutine + Observe/Crawled/Close) — pattern tương tự StallWatchdog v1 (`stall.go`).
2. `dler.go`: thêm field `meter` vào `eofAwareClient` + đo thời gian trong `UploadGetFile` + hàm `NewDownloaderWithMeter`. Giữ `NewDownloader` cũ delegate với meter nil.
3. 3 file task (`tfile/execute.go`, `tfile/stream.go`, `batchtfile/execute.go`): khởi tạo meter cùng chỗ khởi tạo watchdog v1 (cùng parent/cancel), truyền vào downloader qua `NewDownloaderWithMeter`. Không đụng callback Kick của v1.
4. Lỗi phân biệt: khi task lỗi kèm `meter.Crawled()` → thông báo lỗi dùng chuỗi crawl; v1 giữ chuỗi stall. (Kiểm tra qua hàm `dlutil.ErrKind(err)` hoặc biến sentinel đơn giản.)
5. Tests `floodwait_meter_test.go` (window 100-200ms, threshold 20-50ms cho test nhanh): (a) chuỗi RPC chậm liên tục ≥50% window → cancel + Crawled; (b) RPC nhanh liên tục → sống; (c) 1 window chậm + window sau nhanh → sống (window độc lập); (d) RPC chậm nhưng err ≠ nil → không tính; (e) parent cancel → không set Crawled; (f) race: Observe đồng thời nhiều goroutine (`go test -race`).

## Verify

```bash
gofmt -w common/utils/dlutil/ common/tdler/ core/tasks/tfile/ core/tasks/batchtfile/
go build ./... && go vet ./...
go test -race ./common/utils/dlutil/ ./common/tdler/... ./core/tasks/...
```

Chấp nhận:
- Mô phỏng crawl (fake client sleep 4 phút/RPC) → bị cắt trong window đầu với lỗi "download crawling: flood-wait absorbed ...".
- Download khỏe (RPC < 2 phút) không bao giờ bị cắt oan; chết im tuyệt đối vẫn do v1 cắt đúng 15 phút (regression: test v1 stall_test.go phải xanh nguyên vẹn).
- Hai loại lỗi hiển thị phân biệt được trong log + progress message.

## Deploy
Có thể deploy độc lập ngay khi xong (runbook phase 5), không cần chờ phase 1–4.
