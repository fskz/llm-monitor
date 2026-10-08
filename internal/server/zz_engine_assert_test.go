package server

import (
	"errors"
	"net/http"

	"llm-monitor/internal/engine"
	"llm-monitor/internal/store"
)

// engineAdapter proves the parallel-developed *engine.Engine plugs into the
// server without any code change in either package: every EngineAPI and
// ProviderMutator method maps directly, and ErrInFlight is translated via
// errors.Is as the dispatch contract prescribes.
type engineAdapter struct {
	*engine.Engine
}

func (a engineAdapter) IsInFlight(err error) bool { return errors.Is(err, engine.ErrInFlight) }

// Compile-time proof that main can pass an adapted engine to New.
var (
	_ EngineAPI                                       = engineAdapter{}
	_ ProviderMutator                                 = engineAdapter{}
	_ func(*store.Store, *http.Client) *engine.Engine = engine.New
)
