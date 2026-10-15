package typeresolution

import (
	"sync"

	"compiler/internal/module"
	"compiler/internal/moduleid"
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
	target    target.Info
	modules   moduleLookup
	instances map[string]namedTypeInstance
	mu        sync.RWMutex
}

func New(compilerTarget target.Info, modules moduleLookup) *Resolver {
	if !compilerTarget.IsValid() {
		compilerTarget = target.Host()
	}
	return &Resolver{
		target:    compilerTarget,
		modules:   modules,
		instances: make(map[string]namedTypeInstance),
	}
}

// ResetModule removes the generic instances declared by mod. Module.ResetToPhase
// owns artifact invalidation; this method owns only live instance caches.
func (r *Resolver) ResetModule(mod *module.Module) {
	if r == nil || mod == nil {
		return
	}
	r.mu.Lock()
	for identity, instance := range r.instances {
		if owner, _, ok := moduleid.ParseTypeIdentity(identity); ok && owner == mod.ID {
			if !instance.isComplete && instance.ready != nil {
				close(instance.ready)
			}
			delete(r.instances, identity)
		}
	}
	r.mu.Unlock()
}
