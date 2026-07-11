package lsp

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"compiler/internal/diagnostics"
	driver "compiler/internal/driver"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

const completionSentinel = "__peeper_completion__"

const (
	completionKindText     = 1
	completionKindMethod   = 2
	completionKindFunction = 3
	completionKindField    = 5
	completionKindVariable = 6
	completionKindClass    = 7
	completionKindModule   = 9
	completionKindFile     = 17
	completionKindFolder   = 19
	completionKindConstant = 21
)

type completionContextKind uint8

const (
	completionNone completionContextKind = iota
	completionNames
	completionSelector
	completionQualified
	completionImport
)

type parsedCompletionContext struct {
	kind      completionContextKind
	prefix    string
	qualifier string
	start     int
	end       int
	cursor    int
	sentinel  string
}

type completionLexicalState uint8

const (
	completionLexicalCode completionLexicalState = iota
	completionLexicalString
	completionLexicalChar
	completionLexicalLineComment
	completionLexicalBlockComment
)

func (s *ServerState) HandleCompletion(params CompletionParams) ([]CompletionItem, error) {
	if s == nil {
		return []CompletionItem{}, nil
	}
	filePath := uriToPath(string(params.TextDocument.URI))
	sourceText, err := s.completionSource(filePath)
	if err != nil {
		return []CompletionItem{}, nil
	}
	ctx, module := s.currentCompiledModule(filePath)
	if ctx == nil || module == nil || module.Semantics == nil {
		return []CompletionItem{}, nil
	}
	parsed := parseCompletionContext(sourceText, params.Position)
	if parsed.kind == completionNone {
		return []CompletionItem{}, nil
	}
	replacement := Range{Start: positionAtOffset(sourceText, parsed.start), End: positionAtOffset(sourceText, parsed.end)}

	switch parsed.kind {
	case completionImport:
		return importCompletionItems(ctx.ImportCandidates(parsed.prefix, filePath), replacement), nil
	case completionQualified:
		return qualifiedCompletionItems(ctx, module, parsed.qualifier, parsed.prefix, replacement), nil
	case completionSelector:
		sentinelCtx, sentinelModule := compileCompletionSource(ctx.Config, s.completionOverlays(filePath), filePath, parsed.sentinel)
		if sentinelCtx == nil || sentinelModule == nil || sentinelModule.Semantics == nil {
			return []CompletionItem{}, nil
		}
		return selectorCompletionItems(sentinelCtx, sentinelModule, parsed.prefix, replacement), nil
	case completionNames:
		semanticCursor := source.NewPosition()
		semanticCursor.Advance(sourceText[:parsed.cursor])
		return lexicalCompletionItems(module, semanticCursor, parsed.prefix, replacement), nil
	default:
		return []CompletionItem{}, nil
	}
}

func (s *ServerState) completionSource(filePath string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return workspaceContent(project.CanonicalPath(filePath), s.Cache)
}

func (s *ServerState) completionOverlays(currentFile string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	overlays := make(map[string]string, len(s.Cache))
	for filePath, content := range s.Cache {
		if project.CanonicalPath(filePath) != project.CanonicalPath(currentFile) {
			overlays[filePath] = content
		}
	}
	return overlays
}

func compileCompletionSource(cfg project.Config, overlays map[string]string, filePath, content string) (*project.CompilerContext, *project.Module) {
	ctx := driver.NewContext(cfg, diagnostics.NewDiagnosticBag())
	for overlayPath, overlayContent := range overlays {
		driver.AddOverlay(ctx, overlayPath, overlayContent)
	}
	return ctx, driver.ParseFileWithOverlay(ctx, filePath, content)
}

func parseCompletionContext(text string, position Position) parsedCompletionContext {
	offset, ok := offsetAtPosition(text, position)
	if !ok {
		return parsedCompletionContext{}
	}
	lexicalState, quoteStart := cursorLexicalState(text, offset)
	if lexicalState != completionLexicalCode && lexicalState != completionLexicalString {
		return parsedCompletionContext{}
	}
	if lexicalState == completionLexicalString {
		if !isImportQuote(text, quoteStart) {
			return parsedCompletionContext{}
		}
		end := importContentEnd(text, offset)
		return parsedCompletionContext{
			kind:   completionImport,
			prefix: text[quoteStart+1 : offset],
			start:  quoteStart + 1,
			end:    end,
			cursor: offset,
		}
	}

	start := offset
	for start > 0 && isIdentifierByte(text[start-1]) {
		start--
	}
	end := offset
	for end < len(text) && isIdentifierByte(text[end]) {
		end++
	}
	prefix := text[start:offset]
	if start > 0 && text[start-1] == '.' {
		sentinel := text[:start] + completionSentinel + text[end:]
		return parsedCompletionContext{kind: completionSelector, prefix: prefix, start: start, end: end, cursor: offset, sentinel: sentinel}
	}
	if start >= 2 && text[start-2:start] == "::" {
		qualifierEnd := start - 2
		qualifierStart := qualifierEnd
		for qualifierStart > 0 && isIdentifierByte(text[qualifierStart-1]) {
			qualifierStart--
		}
		if qualifierStart == qualifierEnd {
			return parsedCompletionContext{}
		}
		return parsedCompletionContext{kind: completionQualified, prefix: prefix, qualifier: text[qualifierStart:qualifierEnd], start: start, end: end, cursor: offset}
	}
	if prefix == "" && offset > 0 && !isCompletionBoundary(text[offset-1]) {
		return parsedCompletionContext{}
	}
	return parsedCompletionContext{kind: completionNames, prefix: prefix, start: start, end: end, cursor: offset}
}

func cursorLexicalState(text string, offset int) (completionLexicalState, int) {
	state := completionLexicalCode
	quoteStart := -1
	for i := 0; i < offset; i++ {
		switch state {
		case completionLexicalCode:
			switch {
			case text[i] == '"':
				state = completionLexicalString
				quoteStart = i
			case text[i] == '\'':
				state = completionLexicalChar
				quoteStart = i
			case i+1 < offset && text[i:i+2] == "//":
				state = completionLexicalLineComment
				i++
			case i+1 < offset && text[i:i+2] == "/*":
				state = completionLexicalBlockComment
				i++
			}
		case completionLexicalString:
			if text[i] == '\\' {
				i++
			} else if text[i] == '"' {
				state = completionLexicalCode
				quoteStart = -1
			}
		case completionLexicalChar:
			if text[i] == '\\' {
				i++
			} else if text[i] == '\'' {
				state = completionLexicalCode
				quoteStart = -1
			}
		case completionLexicalLineComment:
			if text[i] == '\n' {
				state = completionLexicalCode
			}
		case completionLexicalBlockComment:
			if i+1 < offset && text[i:i+2] == "*/" {
				state = completionLexicalCode
				i++
			}
		}
	}
	return state, quoteStart
}

func isImportQuote(text string, quoteStart int) bool {
	if quoteStart < 0 {
		return false
	}
	before := strings.TrimSpace(text[:quoteStart])
	end := len(before)
	start := end
	for start > 0 && isIdentifierByte(before[start-1]) {
		start--
	}
	return before[start:end] == "import"
}

func importContentEnd(text string, offset int) int {
	for i := offset; i < len(text); i++ {
		if text[i] == '\\' {
			i++
			continue
		}
		if text[i] == '"' || text[i] == '\n' {
			return i
		}
	}
	return offset
}

func isIdentifierByte(ch byte) bool {
	return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
}

func isCompletionBoundary(ch byte) bool {
	switch ch {
	case ' ', '\t', '\r', '\n', '(', '{', '[', ',', ';', '=', ':':
		return true
	default:
		return false
	}
}

func lexicalCompletionItems(module *project.Module, cursor source.Position, prefix string, replacement Range) []CompletionItem {
	if module == nil || module.ModuleScope == nil || module.Semantics == nil {
		return []CompletionItem{}
	}
	scope := completionScope(module, cursor.Line, cursor.Column)
	seen := make(map[string]struct{})
	var items []CompletionItem
	localScope := scope != module.ModuleScope
	for current := scope; current != nil; current = current.Parent() {
		for _, sym := range current.Symbols() {
			if sym == nil || sym.Name == "_" || !strings.HasPrefix(sym.Name, prefix) {
				continue
			}
			if localScope && declaredAfterCursor(sym, cursor) {
				continue
			}
			if _, exists := seen[sym.Name]; exists {
				continue
			}
			seen[sym.Name] = struct{}{}
			items = append(items, symbolCompletionItem(sym, replacement))
		}
		if current == module.ModuleScope {
			localScope = false
		}
	}
	return sortCompletionItems(items)
}

func completionScope(module *project.Module, line, col int) *table.Scope {
	scope := module.ModuleScope
	walkModuleAST(module, func(node ast.Node, _ ast.Node) bool {
		block, ok := node.(*ast.BlockStmt)
		if !ok || !locContains(ast.LocOf(block), line, col) {
			return true
		}
		if blockScope := module.Semantics.BlockScopes[block.ID()]; blockScope != nil {
			scope = blockScope
		}
		return true
	})
	return scope
}

func declaredAfterCursor(sym *symbols.Symbol, cursor source.Position) bool {
	if sym == nil || sym.Location == nil || sym.Location.Start == nil {
		return false
	}
	start := sym.Location.Start
	return start.Line > cursor.Line || start.Line == cursor.Line && start.Column > cursor.Column
}

func qualifiedCompletionItems(ctx *project.CompilerContext, module *project.Module, qualifier, prefix string, replacement Range) []CompletionItem {
	if ctx == nil || module == nil {
		return []CompletionItem{}
	}
	resolved, ok := module.Imports[qualifier]
	if !ok || resolved.DependencyAlias != "" {
		return []CompletionItem{}
	}
	imported, ok := ctx.ModuleByKey(resolved.Key)
	if !ok || imported == nil || imported.ModuleScope == nil {
		return []CompletionItem{}
	}
	var items []CompletionItem
	for _, sym := range imported.ModuleScope.Symbols() {
		if sym != nil && sym.IsPub && strings.HasPrefix(sym.Name, prefix) {
			items = append(items, symbolCompletionItem(sym, replacement))
		}
	}
	return sortCompletionItems(items)
}

func selectorCompletionItems(ctx *project.CompilerContext, module *project.Module, prefix string, replacement Range) []CompletionItem {
	var selector *ast.SelectorExpr
	parents := make(map[ast.NodeID]ast.Node)
	walkModuleAST(module, func(node ast.Node, parent ast.Node) bool {
		if parent != nil {
			parents[node.ID()] = parent
		}
		candidate, ok := node.(*ast.SelectorExpr)
		if ok && candidate.Name != nil && candidate.Name.Name == completionSentinel {
			selector = candidate
		}
		return true
	})
	if selector == nil {
		return []CompletionItem{}
	}
	baseType, ok := selectorBaseType(selector.Expr, parents, module, ctx)
	if !ok {
		return []CompletionItem{}
	}

	seen := make(map[string]struct{})
	var items []CompletionItem
	fieldType := baseType
	if target, ok := typeinfo.PointerTarget(fieldType); ok {
		fieldType = target
	} else if target, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(fieldType)); ok {
		fieldType = target
	}
	if strct, ok := typeinfo.Underlying(fieldType).(*typeinfo.StructType); ok && strct != nil {
		for _, field := range strct.Fields {
			if !strings.HasPrefix(field.Name, prefix) {
				continue
			}
			seen[field.Name] = struct{}{}
			items = append(items, CompletionItem{
				Label:    field.Name,
				Kind:     completionKindField,
				Detail:   fmt.Sprintf("(field) %s: %s", field.Name, typeinfo.TypeText(field.Type)),
				TextEdit: TextEdit{Range: replacement, NewText: field.Name},
			})
		}
	}
	if iface, ok := typeinfo.InterfaceTypeOf(baseType); ok {
		for _, method := range iface.Methods {
			if method.Name == "" || !strings.HasPrefix(method.Name, prefix) {
				continue
			}
			params := make([]typeinfo.Type, len(method.Params))
			for i, param := range method.Params {
				params[i] = param.Type
			}
			seen[method.Name] = struct{}{}
			methodType := &typeinfo.FuncType{Params: params, Return: method.Return}
			items = append(items, CompletionItem{
				Label:    method.Name,
				Kind:     completionKindMethod,
				Detail:   fmt.Sprintf("(method) %s: %s", method.Name, typeinfo.TypeText(methodType)),
				TextEdit: TextEdit{Range: replacement, NewText: method.Name},
			})
		}
	}
	for _, key := range typeinfo.GetMethodLookupKeys(baseType) {
		for _, method := range module.Semantics.MethodSets[key] {
			if method == nil || !strings.HasPrefix(method.Name, prefix) {
				continue
			}
			if _, exists := seen[method.Name]; exists {
				continue
			}
			seen[method.Name] = struct{}{}
			items = append(items, symbolCompletionItem(method, replacement))
		}
	}
	return sortCompletionItems(items)
}

func importCompletionItems(candidates []project.ImportCandidate, replacement Range) []CompletionItem {
	items := make([]CompletionItem, 0, len(candidates))
	for _, candidate := range candidates {
		kind := completionKindFile
		sortPrefix := "1"
		if candidate.Continuing {
			kind = completionKindFolder
			sortPrefix = "0"
		}
		items = append(items, CompletionItem{
			Label:    candidate.ImportPath,
			Kind:     kind,
			Detail:   candidate.FilePath,
			SortText: sortPrefix + candidate.ImportPath,
			TextEdit: TextEdit{Range: replacement, NewText: candidate.ImportPath},
		})
	}
	return items
}

func symbolCompletionItem(sym *symbols.Symbol, replacement Range) CompletionItem {
	item := CompletionItem{
		Label:    sym.Name,
		Kind:     completionItemKind(sym.Kind),
		Detail:   "(" + string(sym.Kind) + ") " + sym.Name,
		TextEdit: TextEdit{Range: replacement, NewText: sym.Name},
	}
	if typ, ok := symbols.GetSymbolType(sym); ok && typ != nil && !typeinfo.IsInvalidOrUnknown(typ) {
		item.Detail += ": " + typeinfo.TypeText(typ)
	}
	return item
}

func completionItemKind(kind symbols.Kind) int {
	switch kind {
	case symbols.SymbolMethod:
		return completionKindMethod
	case symbols.SymbolFunc:
		return completionKindFunction
	case symbols.SymbolField:
		return completionKindField
	case symbols.SymbolVar, symbols.SymbolParam:
		return completionKindVariable
	case symbols.SymbolType:
		return completionKindClass
	case symbols.SymbolImport:
		return completionKindModule
	case symbols.SymbolConst, symbols.SymbolStatic, symbols.SymbolVariant:
		return completionKindConstant
	default:
		return completionKindText
	}
}

func sortCompletionItems(items []CompletionItem) []CompletionItem {
	slices.SortFunc(items, func(a, b CompletionItem) int {
		if cmp := strings.Compare(a.Label, b.Label); cmp != 0 {
			return cmp
		}
		return a.Kind - b.Kind
	})
	return items
}

func offsetAtPosition(text string, position Position) (int, bool) {
	if position.Line < 0 || position.Character < 0 {
		return 0, false
	}
	lineStart := 0
	for line := 0; line < position.Line; line++ {
		newline := strings.IndexByte(text[lineStart:], '\n')
		if newline < 0 {
			return 0, false
		}
		lineStart += newline + 1
	}
	lineEnd := len(text)
	if newline := strings.IndexByte(text[lineStart:], '\n'); newline >= 0 {
		lineEnd = lineStart + newline
	}
	units := 0
	for offset := lineStart; offset < lineEnd; {
		if units == position.Character {
			return offset, true
		}
		r, size := utf8.DecodeRuneInString(text[offset:lineEnd])
		runeUnits := 1
		if r > 0xffff {
			runeUnits = 2
		}
		if units+runeUnits > position.Character {
			return 0, false
		}
		units += runeUnits
		offset += size
	}
	if units == position.Character {
		return lineEnd, true
	}
	return 0, false
}

func positionAtOffset(text string, offset int) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	lineStart := strings.LastIndexByte(text[:offset], '\n') + 1
	return Position{
		Line:      strings.Count(text[:lineStart], "\n"),
		Character: len(utf16.Encode([]rune(text[lineStart:offset]))),
	}
}
