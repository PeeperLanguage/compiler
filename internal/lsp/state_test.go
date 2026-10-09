package lsp

import (
	"compiler/internal/module"
	"compiler/internal/project"
)

// recompile is the locked single-entry compile that tests and benchmarks drive.
// Production code calls recompileLocked while already holding the lock.
func (s *ServerState) recompile(entryFile string) (*project.CompilerContext, *module.Module) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recompileLocked(entryFile, nil)
}
