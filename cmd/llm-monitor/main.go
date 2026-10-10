package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"llm-monitor/internal/engine"
	"llm-monitor/internal/server"
	"llm-monitor/internal/store"
	"llm-monitor/internal/tui"
	"llm-monitor/web"
)

const (
	defaultPort = 10110
	appDirName  = "llm-monitor"
)

// dataDir returns the per-user writable data directory (§5.2): the OS user
// config dir, falling back to HOME when UserConfigDir cannot resolve.
func dataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("cannot locate user config dir or home: %v / %v", err, herr)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, appDirName), nil
}

func main() {
	port := flag.Int("port", defaultPort, "按需 Web 面板的起始扫描端口（TUI 内拉起时从该端口向上查找可用端口）")
	flag.Parse()

	log.SetFlags(log.LstdFlags)

	dir, err := dataDir()
	if err != nil {
		log.Fatalf("locate data directory: %v", err)
	}
	st, err := store.New(dir)
	if err != nil {
		log.Fatalf("open store at %s: %v (config.json may be corrupt; fix or remove it, then restart)", dir, err)
	}

	// Engine wiring unchanged: one process owns the engine and the store
	// (task 10-09 R1). engineAdapter mirrors the compile-time assert in
	// internal/server: ProbeNow errors map ErrInFlight to 409 semantics.
	eng := engine.New(st, &http.Client{})
	adapter := engineAdapter{eng}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	eng.Start(ctx)

	// The TUI blocks the main goroutine with ZERO listening ports (R1);
	// the web panel is pulled up on demand from inside the TUI (R2). The
	// handler factory keeps tui free of any internal/server import —
	// dependency direction stays cmd → server → view, cmd → tui → view.
	if err := tui.Run(ctx, tui.Deps{
		Store:     st,
		Engine:    adapter,
		Mutator:   adapter,
		PortStart: *port,
		ServerHook: func(boundPort int) http.Handler {
			return server.New(st, adapter, adapter, web.FS(), boundPort).Handler()
		},
	}); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("tui: %v", err)
	}

	log.Println("正在退出：停止调度并取消在途请求……")
	eng.Shutdown(10 * time.Second)
	log.Println("已退出。")
}

// engineAdapter adapts *engine.Engine to the shared view interfaces
// (view.EngineAPI / view.ProviderMutator).
type engineAdapter struct {
	*engine.Engine
}

func (a engineAdapter) IsInFlight(err error) bool { return errors.Is(err, engine.ErrInFlight) }
