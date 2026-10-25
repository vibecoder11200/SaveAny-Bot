package handlers

import (
	"os"
	"path/filepath"
	"time"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/ext"
	"github.com/charmbracelet/log"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/i18n/i18nk"
	"github.com/krau/SaveAny-Bot/common/tdler"
	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
	"github.com/krau/SaveAny-Bot/common/utils/fsutil"
	"github.com/krau/SaveAny-Bot/config"
	storcfg "github.com/krau/SaveAny-Bot/config/storage"
	"github.com/krau/SaveAny-Bot/core"
	"github.com/krau/SaveAny-Bot/database"
)

// handleStatusCmd reports the bot's operational state: version, uptime,
// workers, queue depth, cache and resume sizes, disk free for local roots,
// watch count and the database size. Global stats would leak deployment
// details to any whitelisted account, so the command is additionally gated
// to the FIRST whitelisted user (the deployment owner).
func handleStatusCmd(ctx *ext.Context, update *ext.Update) error {
	users := config.C().GetUsersID()
	if len(users) == 0 || update.GetUserChat().GetID() != users[0] {
		ctx.Reply(update, ext.ReplyTextString(i18n.T(i18nk.BotMsgStatusNotPermitted)), nil)
		return dispatcher.EndGroups
	}
	logger := log.FromContext(ctx)

	uptime := time.Since(core.StartTime()).Round(time.Second)
	queued := core.GetQueuedTasks(ctx)
	var running int
	for _, t := range core.GetRunningTasks(ctx) {
		if !t.Cancelled {
			running++
		}
	}
	watchCount, err := database.CountWatchChats(ctx)
	if err != nil {
		logger.Warnf("Failed to count watches: %v", err)
	}

	cachePath := config.C().Temp.BasePath
	cacheSize := measureDirSize(ctx, cachePath, logger)
	resumeSize := measureDirSize(ctx, filepath.Join(cachePath, tdler.ResumeDirName), logger)
	dbSize := fileSizeOrNil(config.C().DB.Path)

	type localRoot struct {
		name string
		path string
		free uint64
	}
	var roots []localRoot
	seenRoots := make(map[string]bool)
	addRoot := func(name, path string) {
		free, ok := diskFreeOf(path, logger)
		if !ok {
			return
		}
		roots = append(roots, localRoot{name: name, path: path, free: free})
		seenRoots[filepath.VolumeName(path)] = true
	}
	addRoot(i18n.T(i18nk.BotMsgStatusCacheLabel), cachePath)
	for _, sc := range config.C().Storages {
		local, ok := sc.(*storcfg.LocalStorageConfig)
		if !ok {
			continue
		}
		if seenRoots[filepath.VolumeName(local.BasePath)] && sameDiskFree(cachePath, local.BasePath) {
			continue
		}
		addRoot(local.Name, local.BasePath)
	}

	storageRows := ""
	for _, r := range roots {
		storageRows += i18n.T(i18nk.BotMsgStatusDiskRow, map[string]any{
			"Name": r.name, "Path": r.path, "Free": dlutil.FormatSize(int64(r.free)),
		})
	}
	if storageRows == "" {
		storageRows = i18n.T(i18nk.BotMsgStatusNoLocalDisks)
	}

	dockerMode := config.Docker == "true"
	text := i18n.T(i18nk.BotMsgStatusBody, map[string]any{
		"Version":    config.Version,
		"Commit":     shortCommit(config.GitCommit),
		"Docker":     dockerMode,
		"Uptime":     uptime.String(),
		"Workers":    config.C().Workers,
		"Queued":     len(queued),
		"Running":    running,
		"CacheSize":  cacheSize,
		"ResumeSize": resumeSize,
		"DiskRows":   storageRows,
		"WatchCount": watchCount,
		"DBSize":     dbSize,
	})
	ctx.Reply(update, ext.ReplyTextString(text), nil)
	return dispatcher.EndGroups
}

// measureDirSize renders a DirSize readout, keeping /status responsive: the
// single-flight cache bounds the work, and any error degrades to "unknown"
// instead of failing the command.
func measureDirSize(ctx *ext.Context, path string, logger *log.Logger) string {
	if path == "" {
		return i18n.T(i18nk.BotMsgStatusUnknown)
	}
	size, err := fsutil.DirSize(path)
	if err != nil {
		logger.Debugf("DirSize(%s): %v", path, err)
		return i18n.T(i18nk.BotMsgStatusUnknown)
	}
	return dlutil.FormatSize(size)
}

func fileSizeOrNil(path string) string {
	if path == "" {
		return i18n.T(i18nk.BotMsgStatusUnknown)
	}
	info, err := os.Stat(path)
	if err != nil {
		return i18n.T(i18nk.BotMsgStatusUnknown)
	}
	return dlutil.FormatSize(info.Size())
}

// diskFreeOf reports free bytes on the filesystem of path; false when the
// syscall fails (path missing, unsupported fs).
func diskFreeOf(path string, logger *log.Logger) (uint64, bool) {
	free, err := fsutil.DiskFree(path)
	if err != nil {
		logger.Debugf("DiskFree(%s): %v", path, err)
		return 0, false
	}
	return free, true
}

// sameDiskFree reports whether two paths share a filesystem by comparing
// their free byte counts — cheap and adequate for de-duplicating /status
// rows on single-disk deployments.
func sameDiskFree(a, b string) bool {
	fa, errA := fsutil.DiskFree(a)
	fb, errB := fsutil.DiskFree(b)
	return errA == nil && errB == nil && fa == fb
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
