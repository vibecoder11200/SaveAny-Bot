package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"slices"

	"github.com/charmbracelet/log"
	"github.com/krau/SaveAny-Bot/api"
	"github.com/krau/SaveAny-Bot/client/bot"
	userclient "github.com/krau/SaveAny-Bot/client/user"
	"github.com/krau/SaveAny-Bot/common/cache"
	"github.com/krau/SaveAny-Bot/common/i18n"
	"github.com/krau/SaveAny-Bot/common/selfupdate"
	"github.com/krau/SaveAny-Bot/common/tdler"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/core"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/parsers"
	"github.com/krau/SaveAny-Bot/storage"
	"github.com/spf13/cobra"
)

func Run(cmd *cobra.Command, _ []string) {
	ctx, cancel := context.WithCancel(cmd.Context())
	logger := log.NewWithOptions(os.Stdout, log.Options{
		Level:           log.InfoLevel,
		ReportTimestamp: true,
		TimeFormat:      time.TimeOnly,
		ReportCaller:    true,
	})
	log.SetDefault(logger)
	ctx = log.WithContext(ctx, logger)
	core.SetStartTime(time.Now())

	selfupdate.CleanLeftovers()

	configFile := config.GetConfigFile(cmd)
	if err := config.Init(ctx, configFile); err != nil {
		logger.Fatal("Init failed", "error", err)
	}

	level, err := log.ParseLevel(strings.TrimSpace(config.C().Log.Level))
	if err != nil {
		logger.Warn("Invalid log level, fallback to debug", "level", config.C().Log.Level, "error", err)
		level = log.DebugLevel
	}
	logger.SetLevel(level)

	exitChan, err := initAll(ctx)
	if err != nil {
		logger.Fatal("Init failed", "error", err)
	}
	go func() {
		<-exitChan
		cancel()
	}()

	// Recover tasks that were in flight during the last stop. Runs in its
	// own goroutine after the bot client is ready; a no-op without pending
	// journal rows.
	if ectx := bot.ExtContext(); ectx != nil {
		go bot.RunRecovery(ctx, ectx)
	}

	core.Run(ctx)

	<-ctx.Done()
	logger.Info("Exiting...")
	defer logger.Info("Exit complete")
	core.Close()
	cleanCache()
	if selfupdate.ShouldRestart() {
		logger.Info("Restarting to run the updated version...")
		if err := selfupdate.Restart(); err != nil {
			logger.Errorf("Failed to restart: %s. Please start saveany-bot again", err)
		}
	}
}

func initAll(ctx context.Context) (<-chan struct{}, error) {
	cache.Init()
	logger := log.FromContext(ctx)
	i18n.Init(config.C().Lang)
	logger.Info("Initializing...")
	// Everything in the cache dir at startup is orphaned: the shutdown
	// clean below never runs after a kill -9 (e.g. a hung task blocking
	// graceful exit past the docker stop timeout), and recovery always
	// creates fresh cache files.
	cleanCache()
	database.Init(ctx)
	storage.LoadStorages(ctx)
	if config.C().Parser.PluginEnable {
		for _, dir := range config.C().Parser.PluginDirs {
			if err := parsers.LoadPlugins(ctx, dir); err != nil {
				logger.Error("Failed to load parser plugins", "dir", dir, "error", err)
			} else {
				logger.Debug("Loaded parser plugins from directory", "dir", dir)
			}
		}
	}
	if config.C().Telegram.Userbot.Enable {
		_, err := userclient.Login(ctx)
		if err != nil {
			logger.Fatal("User login failed", "error", err)
		}
	}
	if err := api.Start(ctx); err != nil {
		logger.Fatal("Failed to start API server", "error", err)
	}
	return bot.Init(ctx), nil
}

func cleanCache() {
	basePath := config.C().Temp.BasePath
	if basePath == "" || config.C().Stream {
		// Stream mode never creates cache or resume state.
		return
	}
	if slices.Contains([]string{"/", ".", "\\", ".."}, filepath.Clean(basePath)) {
		log.Error("Invalid cache directory", "path", basePath)
		return
	}
	// filepath.Abs keeps an absolute BasePath intact — filepath.Join with
	// the working dir would mangle it into a nonexistent path.
	cachePath, err := filepath.Abs(basePath)
	if err != nil {
		log.Error("Failed to get absolute cache path", "error", err)
		return
	}
	if !config.C().NoCleanCache {
		log.Info("Cleaning cache directory", "path", cachePath)
		if err := cleanCacheDir(cachePath); err != nil {
			log.Error("Failed to clean cache directory", "error", err)
		}
	}
	// Resume state accumulates regardless of the no-clean-cache opt-out,
	// so its GC runs even then.
	cleanStaleResumeStates(filepath.Join(cachePath, tdler.ResumeDirName))
}

// cleanCacheDir removes everything inside the cache directory except the
// resume subdirectory: interrupted-download state must survive the boot
// clean — that is the entire point of resumable downloads. It also drops
// the sibling dir the 0.66.1 layout used, where state stranded outside the
// cache mount could not be renamed back in (EXDEV) and is re-downloadable.
func cleanCacheDir(cachePath string) error {
	entries, err := os.ReadDir(cachePath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == tdler.ResumeDirName {
			continue
		}
		if err := os.RemoveAll(filepath.Join(cachePath, entry.Name())); err != nil {
			return err
		}
	}
	_ = os.RemoveAll(cachePath + "-resume")
	return nil
}

// cleanStaleResumeStates prunes resume .part/.bitmap files that have not been
// touched for a week: interrupted downloads that will never be retried again
// (cancelled permanently, destination changed) would otherwise linger in the
// resume dir forever, since nothing else cleans it.
func cleanStaleResumeStates(resumePath string) {
	entries, err := os.ReadDir(resumePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Error("Failed to scan resume directory", "path", resumePath, "error", err)
		}
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(resumePath, entry.Name())); err == nil {
			removed++
		}
	}
	if removed > 0 {
		log.Info("Removed stale resume states", "count", removed, "dir", resumePath)
	}
}
