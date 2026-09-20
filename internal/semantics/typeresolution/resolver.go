package typeresolution

import (
	"sync"

	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	"compiler/internal/target"
)

// moduleLookup is the one project capability semantic type resolution needs:
// canonical module identity lookup. Keeping this contract private prevents it
// from becoming a general compiler context interface.
type moduleLookup interface {
	ModuleByID(moduleid.ID) (*module.Module, bool)
}

// Resolver owns semantic type construction, declaration ownership, and generic
// instance identity for one compiler context.
type Resolver struct {
	target       target.Info
	modules      moduleLookup
	declarations map[string]*module.Module
	instances    map[string]namedTypeInstance
	mu           sync.RWMutex
}

func New(compilerTarget target.Info, modules moduleLookup) *Resolver {
	if !compilerTarget.Valid() {
		compilerTarget = target.Host()
	}
	return &Resolver{
		target:       compilerTarget,
		modules:      modules,
		declarations: make(map[string]*module.Module),
		instances:    make(map[string]namedTypeInstance),
	}
}

// RegisterModule rebuilds declaration ownership when a retained collected
// module enters a fresh incremental compiler context.
func (r *Resolver) RegisterModule(mod *module.Module) {
	if r == nil || mod == nil || mod.Phase < phase.Collected {
		return
	}
	r.mu.Lock()
	for _, identity := range mod.TypeDeclarationIdentities() {
		r.declarations[identity] = mod
	}
	r.mu.Unlock()
}

// ResetModule removes derived type state owned by mod. Module.ResetToPhase owns
// artifact invalidation; this method owns only resolver indexes and live caches.
func (r *Resolver) ResetModule(mod *module.Module, retained phase.Phase) {
	if r == nil || mod == nil {
		return
	}
	r.mu.Lock()
	for identity, instance := range r.instances {
		if instance.ownerModuleID == mod.ID {
			if !instance.complete && instance.ready != nil {
				close(instance.ready)
			}
			delete(r.instances, identity)
		}
	}
	if retained < phase.Collected {
		for identity, owner := range r.declarations {
			if owner != nil && owner.ID == mod.ID {
				delete(r.declarations, identity)
			}
		}
	}
	r.mu.Unlock()
}
