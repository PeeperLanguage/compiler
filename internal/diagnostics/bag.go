package diagnostics

import (
	"bytes"
	"compiler/internal/phase"
	"compiler/internal/source"
	"compiler/pkg/colors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"sync"
)

const (
	compileFailedMsg          = "\nCompilation failed with %d error(s)"
	andWarningMsg             = " and %d warning(s)"
	compileSuccessWithWarning = "\nCompilation succeeded with %d warning(s)\n"
)

// DiagnosticBag collects diagnostics during compilation
type DiagnosticBag struct {
	groups      map[phase.Phase]map[string]diagnosticGroup
	mu          *sync.Mutex
	sourceCache *SourceCache
	phase       phase.Phase
	moduleKey   string
}

type diagnosticGroup struct {
	diagnostics []*Diagnostic
	active      bool
}

// NewDiagnosticBag creates a new diagnostic bag.
func NewDiagnosticBag() *DiagnosticBag {
	return &DiagnosticBag{
		groups:      make(map[phase.Phase]map[string]diagnosticGroup),
		mu:          &sync.Mutex{},
		sourceCache: NewSourceCache(),
	}
}

// BeginPhase replaces one producing phase/module group and returns its writer.
func (db *DiagnosticBag) BeginPhase(producingPhase phase.Phase, moduleKey string) *DiagnosticBag {
	scoped := db.AppendPhase(producingPhase, moduleKey)
	db.mu.Lock()
	if db.groups[producingPhase] == nil {
		db.groups[producingPhase] = make(map[string]diagnosticGroup)
	}
	db.groups[producingPhase][moduleKey] = diagnosticGroup{active: true}
	db.mu.Unlock()
	return scoped
}

// AppendPhase returns a writer that continues one producing phase/module group.
func (db *DiagnosticBag) AppendPhase(producingPhase phase.Phase, moduleKey string) *DiagnosticBag {
	return &DiagnosticBag{
		groups:      db.groups,
		mu:          db.mu,
		sourceCache: db.sourceCache,
		phase:       producingPhase,
		moduleKey:   moduleKey,
	}
}

// DiscardModuleAfter removes diagnostics invalidated by a module phase reset.
func (db *DiagnosticBag) DiscardModuleAfter(moduleKey string, retained phase.Phase) {
	db.mu.Lock()
	defer db.mu.Unlock()
	for producingPhase, modules := range db.groups {
		if producingPhase <= retained {
			continue
		}
		delete(modules, moduleKey)
		if len(modules) == 0 {
			delete(db.groups, producingPhase)
		}
	}
}

// CopyModuleRange replaces one module's destination groups inside an inclusive
// phase range. Inactive groups remain reusable but do not affect compilation or output.
func (db *DiagnosticBag) CopyModuleRange(source *DiagnosticBag, moduleKey string, first, last phase.Phase, active bool) {
	if source == nil || db.mu == source.mu || first > last {
		return
	}
	copiedGroups := make(map[phase.Phase]diagnosticGroup)
	source.mu.Lock()
	for producingPhase, modules := range source.groups {
		if producingPhase < first || producingPhase > last {
			continue
		}
		if group, ok := modules[moduleKey]; ok {
			copiedGroups[producingPhase] = diagnosticGroup{
				diagnostics: append([]*Diagnostic(nil), group.diagnostics...),
				active:      active,
			}
		}
	}
	source.mu.Unlock()

	db.mu.Lock()
	defer db.mu.Unlock()
	for producingPhase, modules := range db.groups {
		if producingPhase < first || producingPhase > last {
			continue
		}
		delete(modules, moduleKey)
		if len(modules) == 0 {
			delete(db.groups, producingPhase)
		}
	}
	for producingPhase, group := range copiedGroups {
		if db.groups[producingPhase] == nil {
			db.groups[producingPhase] = make(map[string]diagnosticGroup)
		}
		db.groups[producingPhase][moduleKey] = group
	}
}

// ActivateModuleRange publishes retained groups after their project barrier succeeds.
func (db *DiagnosticBag) ActivateModuleRange(moduleKey string, first, last phase.Phase) {
	if first > last {
		return
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	for producingPhase, modules := range db.groups {
		if producingPhase < first || producingPhase > last {
			continue
		}
		group, ok := modules[moduleKey]
		if !ok {
			continue
		}
		group.active = true
		modules[moduleKey] = group
	}
}

// AddSourceContent adds source content for a file path (for in-memory compilation)
func (db *DiagnosticBag) AddSourceContent(filepath, content string) {
	db.sourceCache.AddSource(filepath, content)
}

// GetSourceCache returns the source cache for accessing source content
func (db *DiagnosticBag) GetSourceCache() *SourceCache {
	return db.sourceCache
}

// Add adds a diagnostic to the bag. Error diagnostics at the same
// (file, line, column) are deduplicated - only the first is kept.
func (db *DiagnosticBag) Add(diag *Diagnostic) {
	db.mu.Lock()
	defer db.mu.Unlock()

	if diag.Severity == Error && len(diag.Labels) > 0 {
		newLoc := diag.Labels[0].Location
		if newLoc != nil && newLoc.Start != nil {
			for _, modules := range db.groups {
				for _, group := range modules {
					if !group.active {
						continue
					}
					for _, existing := range group.diagnostics {
						if existing.Severity != Error || len(existing.Labels) == 0 {
							continue
						}
						exLoc := existing.Labels[0].Location
						if exLoc == nil || exLoc.Start == nil {
							continue
						}
						sameFile := (newLoc.Filename == nil && exLoc.Filename == nil) ||
							(newLoc.Filename != nil && exLoc.Filename != nil && *newLoc.Filename == *exLoc.Filename)
						if sameFile && newLoc.Start.Line == exLoc.Start.Line && newLoc.Start.Column == exLoc.Start.Column {
							return
						}
					}
				}
			}
		}
	}

	if db.groups[db.phase] == nil {
		db.groups[db.phase] = make(map[string]diagnosticGroup)
	}
	group := db.groups[db.phase][db.moduleKey]
	group.diagnostics = append(group.diagnostics, diag)
	group.active = true
	db.groups[db.phase][db.moduleKey] = group
}

// AddError adds an error diagnostic to the bag and returns it for chaining/customization.
func (db *DiagnosticBag) AddError(code, msg string, loc *source.Location, labelMsg string) *Diagnostic {
	d := NewError(msg).WithCode(code)
	if loc != nil {
		d.WithPrimaryLabel(loc, labelMsg)
	}
	db.Add(d)
	return d
}

// AddWarning adds a warning diagnostic to the bag and returns it for chaining/customization.
func (db *DiagnosticBag) AddWarning(code, msg string, loc *source.Location, labelMsg string) *Diagnostic {
	d := NewWarning(msg).WithCode(code)
	if loc != nil {
		d.WithPrimaryLabel(loc, labelMsg)
	}
	db.Add(d)
	return d
}

// HasErrors returns true if there are any errors
func (db *DiagnosticBag) HasErrors() bool {
	return db.ErrorCount() > 0
}

// ErrorCount returns the number of errors
func (db *DiagnosticBag) ErrorCount() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.countLocked(Error)
}

// WarningCount returns the number of warnings
func (db *DiagnosticBag) WarningCount() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.countLocked(Warning)
}

func (db *DiagnosticBag) countLocked(severity Severity) int {
	count := 0
	for _, modules := range db.groups {
		for _, group := range modules {
			if !group.active {
				continue
			}
			for _, diagnostic := range group.diagnostics {
				if diagnostic != nil && diagnostic.Severity == severity {
					count++
				}
			}
		}
	}
	return count
}

// Diagnostics returns a copy of all diagnostics (thread-safe)
func (db *DiagnosticBag) Diagnostics() []*Diagnostic {
	db.mu.Lock()
	defer db.mu.Unlock()
	phases := make([]phase.Phase, 0, len(db.groups))
	for producingPhase := range db.groups {
		phases = append(phases, producingPhase)
	}
	slices.Sort(phases)
	result := make([]*Diagnostic, 0)
	for _, producingPhase := range phases {
		modules := db.groups[producingPhase]
		moduleKeys := make([]string, 0, len(modules))
		for moduleKey := range modules {
			moduleKeys = append(moduleKeys, moduleKey)
		}
		slices.Sort(moduleKeys)
		for _, moduleKey := range moduleKeys {
			group := modules[moduleKey]
			if group.active {
				result = append(result, group.diagnostics...)
			}
		}
	}
	return result
}

// sortDiagnostics sorts diagnostics by primary label location (file, line, column).
func sortDiagnostics(diagnostics []*Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		iDiag := diagnostics[i]
		jDiag := diagnostics[j]
		if iDiag == nil || jDiag == nil {
			return jDiag != nil
		}

		iLoc := (*source.Location)(nil)
		jLoc := (*source.Location)(nil)
		if len(iDiag.Labels) > 0 {
			iLoc = iDiag.Labels[0].Location
		}
		if len(jDiag.Labels) > 0 {
			jLoc = jDiag.Labels[0].Location
		}

		// No location sorts last.
		if iLoc == nil || iLoc.Start == nil {
			return false
		}
		if jLoc == nil || jLoc.Start == nil {
			return true
		}

		iFile := ""
		jFile := ""
		if iLoc.Filename != nil {
			iFile = *iLoc.Filename
		}
		if jLoc.Filename != nil {
			jFile = *jLoc.Filename
		}
		if iFile != jFile {
			return iFile < jFile
		}
		if iLoc.Start.Line != jLoc.Start.Line {
			return iLoc.Start.Line < jLoc.Start.Line
		}
		if iLoc.Start.Column != jLoc.Start.Column {
			return iLoc.Start.Column < jLoc.Start.Column
		}
		return false
	})
}

func (db *DiagnosticBag) EmitAll() {
	emitter := NewEmitter(os.Stderr)
	db.emitFiltered(emitter, os.Stderr, func(*Diagnostic) bool { return true })
}

// EmitErrors prints only error diagnostics and an error-only summary.
func (db *DiagnosticBag) EmitErrors() {
	emitter := NewEmitter(os.Stderr)
	db.emitFiltered(emitter, os.Stderr, func(diag *Diagnostic) bool {
		return diag != nil && diag.Severity == Error
	})
}

func (db *DiagnosticBag) emitFiltered(emitter *Emitter, w io.Writer, keep func(*Diagnostic) bool) {
	diagnostics := db.Diagnostics()

	filtered := diagnostics[:0]
	var errors, warnings int
	for _, diag := range diagnostics {
		if keep != nil && !keep(diag) {
			continue
		}
		filtered = append(filtered, diag)
		switch diag.Severity {
		case Error:
			errors++
		case Warning:
			warnings++
		}
	}

	// Sort diagnostics by source location
	sortDiagnostics(filtered)

	for _, diag := range filtered {
		emitter.Emit(diag)
	}

	printSummary(w, errors, warnings)
}

// EmitAllToString emits all diagnostics to a string with ANSI codes, using provided source cache
func (db *DiagnosticBag) EmitAllToString() string {
	return db.emitAllToStringWithFormat(colors.LogFormatANSI)
}

// EmitAllToHTML emits all diagnostics to an HTML string, using provided source cache
func (db *DiagnosticBag) EmitAllToHTML() string {
	return db.emitAllToStringWithFormat(colors.LogFormatHTML)
}

func (db *DiagnosticBag) emitAllToStringWithFormat(format colors.LogFormat) string {
	prevFormat := colors.CurrentLogFormat()
	colors.SetLogFormat(format)
	defer colors.SetLogFormat(prevFormat)

	var buf bytes.Buffer
	emitter := &Emitter{
		cache:       db.sourceCache,
		writer:      &buf,
		highlighter: NewSyntaxHighlighter(true),
	}

	db.emitFiltered(emitter, &buf, func(*Diagnostic) bool { return true })

	return buf.String()
}

func printSummary(w io.Writer, errorCount, warnCount int) {
	if errorCount > 0 {
		colors.RED.Fprintf(w, compileFailedMsg, errorCount)
		if warnCount > 0 {
			colors.RED.Fprintf(w, andWarningMsg, warnCount)
		}
		fmt.Fprintln(w)
	} else if warnCount > 0 {
		colors.ORANGE.Fprintf(w, compileSuccessWithWarning, warnCount)
	}
}

// Clear removes all diagnostics
func (db *DiagnosticBag) Clear() {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.groups = make(map[phase.Phase]map[string]diagnosticGroup)
}
