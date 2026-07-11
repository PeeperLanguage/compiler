package project

import "sync"

type CompileMetrics struct {
	mu sync.Mutex

	WorkspaceFiles      int
	WorkspaceModules    int
	WorkspaceComponents int
	DirtyFiles          int
	ModulesParsed       int
	ModulesReused       int
	ModulesDowngraded   int
	PhaseAdvances       int
}

func (m *CompileMetrics) AddWorkspaceSnapshot(files, modules, components int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.WorkspaceFiles = files
	m.WorkspaceModules = modules
	m.WorkspaceComponents = components
}

func (m *CompileMetrics) AddDirtyFiles(count int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DirtyFiles += count
}

func (m *CompileMetrics) AddParsedModule() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ModulesParsed++
}

func (m *CompileMetrics) AddReusedModule() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ModulesReused++
}

func (m *CompileMetrics) AddDowngradedModule() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ModulesDowngraded++
}

func (m *CompileMetrics) AddPhaseAdvance() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PhaseAdvances++
}

func (m *CompileMetrics) Snapshot() CompileMetrics {
	if m == nil {
		return CompileMetrics{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return CompileMetrics{
		WorkspaceFiles:      m.WorkspaceFiles,
		WorkspaceModules:    m.WorkspaceModules,
		WorkspaceComponents: m.WorkspaceComponents,
		DirtyFiles:          m.DirtyFiles,
		ModulesParsed:       m.ModulesParsed,
		ModulesReused:       m.ModulesReused,
		ModulesDowngraded:   m.ModulesDowngraded,
		PhaseAdvances:       m.PhaseAdvances,
	}
}
