// Command ta-daemon is the TypeAnything sidecar process. It owns every
// piece of business logic — input engines (wubi, pinyin), translation
// pipeline, configuration, logging — and exposes them to the in-process
// TSF shim DLL over a Windows named pipe.
//
// Run with no args: starts the IPC server and blocks until SIGINT (or
// OpShutdown from a shim). Stderr goes to %LOCALAPPDATA%\TypeAnything\
// ta-daemon.log via the launcher / installer; for interactive dev just
// run it in a console.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"typeanything/internal/config"
	"typeanything/internal/engine"
	"typeanything/internal/ipc"
	"typeanything/internal/logging"
	"typeanything/internal/translate"
)

// Build-time knobs. Overridden via -ldflags "-X main.defaultRimeDir=..."
// in build.ps1 so the installer can ship a daemon that knows where it
// installed its data files.
var (
	defaultRimeDir  = "" // filled at runtime from %APPDATA%\Rime if empty
	defaultDictPath = "" // path to wubi86.dict.yaml; empty = search
	defaultPipeName = ipc.PipeName
)

func main() {
	pipeName := flag.String("pipe", defaultPipeName, "named pipe path to listen on")
	rimeDir := flag.String("rime", resolveDefaultRimeDir(), "%APPDATA%\\Rime path (config + userdb root)")
	dictPath := flag.String("dict", resolveDefaultDictPath(), "path to wubi86.dict.yaml")
	flag.Parse()

	// Block until SIGINT or shutdown. The returned error is logged but
	// never retried — main exits, and the installer's Run-key entry
	// relaunches us on next login (the daemon is stateless across
	// restarts except for the user-db).
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runDaemon(ctx, *pipeName, *rimeDir, *dictPath); err != nil {
		log.Fatalf("ta-daemon: %v", err)
	}
}

// runDaemon performs the full daemon lifecycle: load the dict, wire up
// config + logging + translate pipeline, start the IPC server, and
// block until ctx is cancelled. Split out of main() so tests can drive
// the daemon in-process with custom paths (see smoke_test.go).
//
// ctx cancellation is the only graceful-shutdown path. SIGINT/SIGTERM,
// OpShutdown-from-shim, and test-stop all funnel through it.
func runDaemon(ctx context.Context, pipeName, rimeDir, dictPath string) error {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stderr)
	log.Printf("ta-daemon starting (pid=%d, pipe=%s, rime=%s, dict=%s)",
		os.Getpid(), pipeName, rimeDir, dictPath)

	// --- Load the wubi engine -------------------------------------------
	// The dict is 137k lines (~3MB) — load takes ~0.5s. We fail hard
	// here (before the pipe opens) because a daemon without a wubi
	// engine is useless; better to surface the error loudly than to
	// silently serve no candidates.
	dict, err := engine.LoadDictFile(dictPath)
	if err != nil {
		return logErrorf("cannot load wubi dict %q: %v. "+
			"Reinstall TypeAnything or pass -dict=<path>.", dictPath, err)
	}
	setGlobalDict(dict)
	log.Printf("ta-daemon: wubi dict loaded: %d entries, %d codes",
		len(dict.Entries), len(dict.ByCode))

	// --- Config + logging + translate pipeline --------------------------
	cfg := config.New(rimeDir)

	logPath := filepath.Join(rimeDir, "typeanything_translate.log")
	rotWriter := logging.NewRotatingWriter(logPath, 1<<20) // 1 MiB rotate
	defer rotWriter.Close()
	translateLog := logging.NewTranslateLogger(rotWriter)

	pipeline := translate.New(cfg, translateLog)
	// Surface missing-API-key / auth-failure to the operator via the
	// log; the C++ launches the ta-settings model page, we don't have
	// a UI handle here so we just log loudly. (The Go ta-settings
	// will poll the daemon's HTTP status endpoint.)
	pipeline.OnMissingAPIKey = func() {
		log.Printf("ta-daemon: WARN no api_key configured; please set it via ta-settings")
	}
	pipeline.OnAuthFailed = func() {
		log.Printf("ta-daemon: WARN LLM auth failed (401/403); api_key may be invalid")
	}

	// --- Build the dispatcher -------------------------------------------
	d, err := newDispatcher(cfg, dict, rimeDir, pipeline)
	if err != nil {
		return logErrorf("dispatcher init failed: %v", err)
	}
	srv := ipc.NewServer(pipeName, d.handle)

	// Periodically flush the user-db so a crash doesn't lose frequency
	// updates. 10s is arbitrary; cheap because Save short-circuits on
	// !dirty. Stops when ctx cancels.
	go flushUserDBLoop(ctx, d, 10*time.Second)

	// Run the server on its own goroutine so this goroutine can wait
	// on ctx. When ctx cancels we Shutdown the server (unblocks Accept),
	// flush the user-db one last time, and return.
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		log.Printf("ta-daemon: shutdown signal received")
	case err := <-errCh:
		log.Printf("ta-daemon: server exited: %v", err)
		return err
	}

	d.flushUserDB()
	if err := srv.Shutdown(); err != nil {
		log.Printf("ta-daemon: shutdown error: %v", err)
	}
	log.Printf("ta-daemon: bye")
	return nil
}

// flushUserDBLoop ticks every interval and saves the per-session user
// dbs. Stops when ctx is cancelled.
func flushUserDBLoop(ctx context.Context, d *dispatcher, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.flushUserDB()
		}
	}
}

// logErrorf logs an error and returns it, so callers can do
// `return logErrorf(...)` without losing the message on the stderr log.
func logErrorf(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	log.Printf("ta-daemon: %v", err)
	return err
}

// resolveDefaultRimeDir returns the conventional %APPDATA%\Rime path.
// Falls back to %LOCALAPPDATA%\Rime if APPDATA is unset (rare — headless
// service accounts). Empty result means "pass -rime on the command line".
func resolveDefaultRimeDir() string {
	if defaultRimeDir != "" {
		return defaultRimeDir
	}
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		if u, err := user.Current(); err == nil && u.HomeDir != "" {
			appdata = filepath.Join(u.HomeDir, "AppData", "Roaming")
		}
	}
	if appdata == "" {
		return ""
	}
	return filepath.Join(appdata, "Rime")
}

// resolveDefaultDictPath searches the conventional locations for the
// shipped wubi86.dict.yaml. The installer drops it into %APPDATA%\Rime\;
// during development we also look in the repo source tree so `go run`
// from a checkout just works.
func resolveDefaultDictPath() string {
	if defaultDictPath != "" {
		return defaultDictPath
	}
	candidates := []string{
		// Production install location.
		filepath.Join(resolveDefaultRimeDir(), "wubi86.dict.yaml"),
		// Repo source tree (dev convenience).
		filepath.Join(findRepoRoot(), "third_party", "weasel", "librime",
			"plugins", "typeanything", "schema", "wubi86.dict.yaml"),
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return filepath.Join(resolveDefaultRimeDir(), "wubi86.dict.yaml")
}

// findRepoRoot walks parents looking for a go.mod identifying the repo.
// Used only for dev-mode dict-path discovery; harmless in production
// (the install tree has no go.mod above %APPDATA%\Rime\).
func findRepoRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	for i := 0; i < 12 && dir != "" && dir != "." && dir != "/" && dir != filepath.Dir(dir); i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}
