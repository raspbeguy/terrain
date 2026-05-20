package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"

	"github.com/raspbeguy/terrain/internal/config"
	"github.com/raspbeguy/terrain/internal/resources"
	"github.com/raspbeguy/terrain/internal/ui"
)

var version = "dev"

func main() {
	var (
		diagnose = flag.Bool("diagnose", false, "load config + backends, print summary, exit (no GUI)")
		debug    = flag.Bool("debug", false, "set log level to debug (also TERRAIN_DEBUG=1)")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("terrain", version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel(*debug),
	}))
	slog.SetDefault(logger)

	slog.Info("starting", "version", version, "go", runtime.Version(), "pid", os.Getpid())

	if err := resources.Register(); err != nil {
		if errors.Is(err, resources.ErrNoResources) {
			slog.Warn("running without embedded gresource bundle; build with `meson compile` for the full UI")
		} else {
			slog.Error("failed to register gresource bundle", "err", err)
			os.Exit(1)
		}
	} else {
		slog.Debug("gresource bundle registered")
	}

	if *diagnose {
		os.Exit(runDiagnose())
	}

	app := ui.NewApp()
	os.Exit(app.Run(flag.Args()))
}

func runDiagnose() int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		return 1
	}
	cfgPath, _ := config.Path()
	slog.Info("config loaded", "path", cfgPath, "backends", len(cfg.Backends))

	backends, err := config.BuildBackends(cfg)
	if err != nil {
		slog.Error("build backends failed", "err", err)
		return 1
	}

	if len(backends) == 0 {
		slog.Info("no backends registered (first-run state)")
	}
	for _, b := range backends {
		ctx, cancel := context.WithCancel(context.Background())
		_ = ctx
		cancel()
		slog.Info("backend",
			"id", b.ID(),
			"kind", b.Kind(),
			"name", b.DisplayName(),
			"caps", b.Capabilities(),
		)
	}
	slog.Info("diagnose ok")
	return 0
}

func logLevel(debug bool) slog.Level {
	if debug || envBool("TERRAIN_DEBUG") {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func envBool(key string) bool {
	v := os.Getenv(key)
	if v == "" {
		return false
	}
	b, _ := strconv.ParseBool(v)
	return b
}
