package server

import (
	"errors"
	"net/http"

	"llm-monitor/internal/engine"
	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// engineAdapter proves the parallel-developed *engine.Engine plugs into the
// shared view interfaces without any code change in either package: every
// EngineAPI and ProviderMutator method maps directly, and ErrInFlight is
// translated via errors.Is as the dispatch contract prescribes.
type engineAdapter struct {
	*engine.Engine
}

func (a engineAdapter) IsInFlight(err error) bool { return errors.Is(err, engine.ErrInFlight) }

// Compile-time proof that main can pass an adapted engine to New, and that
// the shared view package consumes the engine slice structurally (the server
// hands its EngineAPI straight to view.ViewOf without an adapter, and
// EngineAPI subsumes the read-only EngineStatus).
var (
	_ view.EngineAPI                                  = engineAdapter{}
	_ view.ProviderMutator                            = engineAdapter{}
	_ view.EngineStatus                               = view.EngineAPI(nil)
	_ func(*store.Store, *http.Client) *engine.Engine = engine.New
)
