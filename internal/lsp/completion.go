package lsp

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"compiler/internal/diagnostics"
	driver "compiler/internal/driver"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typechecker"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
	"compiler/pkg/ascii"
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
	completionOperation
	completionQualified
	completionImport
)

type parsedCompletionContext struct {
	kind         completionContextKind
	prefix       string
	qualifier    string
	start        int
	end          int
	rewriteStart int
	cursor       int
	pipe         bool
	sentinel     string
	sentinelAt   int
	callSuffix   completionCallSuffixKind
}

type completionCallSuffixKind uint8

const (
	completionCallAbsent completionCallSuffixKind = iota
	completionCallEmpty
	completionCallArguments
)

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
	case completionOperation:
		sentinelCtx, sentinelModule := compileCompletionSource(ctx.Config, s.completionOverlays(filePath), filePath, parsed.sentinel)
		if sentinelCtx == nil || sentinelModule == nil || sentinelModule.Semantics == nil {
			return []CompletionItem{}, nil
		}
		rewrite := Range{Start: positionAtOffset(sourceText, parsed.rewriteStart), End: replacement.End}
		sentinelPosition := positionAtOffset(parsed.sentinel, parsed.sentinelAt)
		return operationCompletionItems(sentinelCtx, sentinelModule, sentinelPosition, parsed.prefix, replacement, rewrite, parsed.pipe, parsed.callSuffix == completionCallArguments), nil
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
	ctx := driver.NewCompilerContext(cfg, diagnostics.NewDiagnosticBag())
	for overlayPath, overlayContent := range overlays {
		driver.AddSource(ctx, overlayPath, overlayContent)
	}
	return ctx, driver.CompileFile(ctx, filePath, content)
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
	identifierEnd := offset
	for identifierEnd < len(text) && isIdentifierByte(text[identifierEnd]) {
		identifierEnd++
	}
	prefix := text[start:offset]
	callSuffix, callEnd := completionCallSuffix(text, identifierEnd)
	editEnd := identifierEnd
	if callSuffix == completionCallEmpty {
		editEnd = callEnd
	}
	if start > 0 && text[start-1] == '.' {
		sentinel := text[:start] + completionSentinel + text[identifierEnd:]
		return parsedCompletionContext{kind: completionOperation, prefix: prefix, start: start, end: editEnd, rewriteStart: start - 1, cursor: offset, sentinel: sentinel, sentinelAt: start, callSuffix: callSuffix}
	}
	operatorEnd := start
	for operatorEnd > 0 && (text[operatorEnd-1] == ' ' || text[operatorEnd-1] == '\t') {
		operatorEnd--
	}
	if operatorEnd >= 2 && text[operatorEnd-2:operatorEnd] == "|>" {
		rewriteStart := operatorEnd - 2
		for rewriteStart > 0 && (text[rewriteStart-1] == ' ' || text[rewriteStart-1] == '\t') {
			rewriteStart--
		}
		sentinelCall := ""
		if callSuffix == completionCallAbsent {
			sentinelCall = "()"
		}
		sentinel := text[:start] + completionSentinel + sentinelCall + text[identifierEnd:]
		return parsedCompletionContext{kind: completionOperation, prefix: prefix, start: start, end: editEnd, rewriteStart: rewriteStart, cursor: offset, pipe: true, sentinel: sentinel, sentinelAt: start, callSuffix: callSuffix}
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
		return parsedCompletionContext{kind: completionQualified, prefix: prefix, qualifier: text[qualifierStart:qualifierEnd], start: start, end: identifierEnd, cursor: offset}
	}
	if prefix == "" && offset > 0 && !isCompletionBoundary(text[offset-1]) {
		return parsedCompletionContext{}
	}
	return parsedCompletionContext{kind: completionNames, prefix: prefix, start: start, end: identifierEnd, cursor: offset}
}

func cursorLexicalState(text string, offset int) (completionLexicalState, int) {
	state := completionLexicalCode
	quoteStart := -1
	for i := 0; i < offset; {
		previous := state
		state, i = advanceCompletionLexicalState(text, i, offset, state)
		if previous == completionLexicalCode && (state == completionLexicalString || state == completionLexicalChar) {
			quoteStart = i - 1
		} else if (previous == completionLexicalString || previous == completionLexicalChar) && state == completionLexicalCode {
			quoteStart = -1
		}
	}
	return state, quoteStart
}

func advanceCompletionLexicalState(text string, index, limit int, state completionLexicalState) (completionLexicalState, int) {
	next := index + 1
	switch state {
	case completionLexicalCode:
		switch {
		case text[index] == '"':
			state = completionLexicalString
		case text[index] == '\'':
			state = completionLexicalChar
		case index+1 < limit && text[index:index+2] == "//":
			state = completionLexicalLineComment
			next++
		case index+1 < limit && text[index:index+2] == "/*":
			state = completionLexicalBlockComment
			next++
		}
	case completionLexicalString:
		if text[index] == '\\' && next < limit {
			next++
		} else if text[index] == '"' {
			state = completionLexicalCode
		}
	case completionLexicalChar:
		if text[index] == '\\' && next < limit {
			next++
		} else if text[index] == '\'' {
			state = completionLexicalCode
		}
	case completionLexicalLineComment:
		if text[index] == '\n' {
			state = completionLexicalCode
		}
	case completionLexicalBlockComment:
		if index+1 < limit && text[index:index+2] == "*/" {
			state = completionLexicalCode
			next++
		}
	}
	return state, next
}

func completionCallSuffix(text string, identifierEnd int) (completionCallSuffixKind, int) {
	open := identifierEnd
	for open < len(text) && (text[open] == ' ' || text[open] == '\t') {
		open++
	}
	if open == len(text) || text[open] != '(' {
		return completionCallAbsent, identifierEnd
	}
	depth := 0
	state := completionLexicalCode
	for i := open; i < len(text); {
		if state == completionLexicalCode {
			switch text[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					if strings.TrimSpace(text[open+1:i]) == "" {
						return completionCallEmpty, i + 1
					}
					return completionCallArguments, i + 1
				}
			}
		}
		state, i = advanceCompletionLexicalState(text, i, len(text), state)
	}
	return completionCallArguments, identifierEnd
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
	return ascii.IsAlnum(rune(ch)) || ch == '_'
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

func operationCompletionItems(ctx *project.CompilerContext, module *project.Module, sentinelPosition Position, prefix string, replacement, rewrite Range, pipe, preserveArguments bool) []CompletionItem {
	var selector *ast.SelectorExpr
	var piped *ast.CallExpr
	cursor := buildCursorContext(ctx, module, sentinelPosition.Line+1, sentinelPosition.Character+1)
	if cursor == nil {
		return []CompletionItem{}
	}
	for node := cursor.node; node != nil; node = cursor.parents[node.ID()] {
		if candidate, ok := node.(*ast.SelectorExpr); ok && candidate.Name != nil && candidate.Name.Name == completionSentinel {
			selector = candidate
			break
		}
		if call, ok := node.(*ast.CallExpr); ok && call.Piped {
			callee, identifier := call.Callee.(*ast.Ident)
			if identifier && callee.Name == completionSentinel {
				piped = call
				break
			}
		}
	}
	var base ast.Expr
	if selector != nil {
		base = selector.Expr
	} else if piped != nil && len(piped.Args) > 0 {
		base = piped.Args[0]
	} else {
		return []CompletionItem{}
	}
	baseType, ok := selectorBaseType(base, cursor.parents, module, ctx)
	if !ok {
		return []CompletionItem{}
	}

	seen := make(map[string]struct{})
	var items []CompletionItem
	if !pipe {
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
				fieldSymbol := &symbols.Symbol{Name: field.Name, Kind: symbols.SymbolField, Type: field.Type}
				items = append(items, CompletionItem{
					Label:    field.Name,
					Kind:     completionKindField,
					Detail:   renderSymbol(fieldSymbol, symbolRenderContext{}),
					SortText: "0" + field.Name,
					TextEdit: TextEdit{Range: replacement, NewText: field.Name},
				})
			}
		}
	}
	if iface, ok := typeinfo.InterfaceTypeOf(baseType); ok {
		for _, method := range iface.Methods {
			if method.Name == "" || !strings.HasPrefix(method.Name, prefix) {
				continue
			}
			fnType, _ := typeinfo.ReplaceAbstractSelf(method.CallableType(), baseType).(*typeinfo.FuncType)
			methodSymbol := &symbols.Symbol{Name: method.Name, Kind: symbols.SymbolMethod, Type: fnType}
			items = appendOperationCompletion(items, seen, methodSymbol, method.Name, fnType, replacement, rewrite, pipe, preserveArguments)
		}
	}
	for _, key := range typeinfo.GetMethodLookupKeys(baseType) {
		for _, method := range module.Semantics.MethodSets[key] {
			if method == nil {
				continue
			}
			fnType, callable := method.Type.(*typeinfo.FuncType)
			if callable && strings.HasPrefix(method.Name, prefix) {
				items = appendOperationCompletion(items, seen, method, method.Name, fnType, replacement, rewrite, pipe, preserveArguments)
			}
		}
	}

	for _, function := range intrinsics.ApplicableFunctionSymbols(baseType, ctx.Target) {
		fnType, callable := function.Type.(*typeinfo.FuncType)
		if callable && strings.HasPrefix(function.Name, prefix) {
			items = appendOperationCompletion(items, seen, function, function.Name, fnType, replacement, rewrite, pipe, preserveArguments)
		}
	}
	for _, function := range operationFunctionsWithPrefix(module.Semantics.OperationFunctions, prefix) {
		fnType, callable := function.Type.(*typeinfo.FuncType)
		if !callable {
			continue
		}
		if typechecker.CanAdaptFirstCallArgument(ctx, module, fnType.Params[0], baseType) {
			items = appendOperationCompletion(items, seen, function, function.Name, fnType, replacement, rewrite, pipe, preserveArguments)
		}
	}
	for alias, resolved := range module.Imports {
		if resolved.DependencyAlias != "" {
			continue
		}
		imported, found := ctx.ModuleByKey(resolved.Key)
		if !found || imported == nil || imported.Semantics == nil {
			continue
		}
		for _, function := range operationFunctionsWithPrefix(imported.Semantics.OperationFunctions, prefix) {
			fnType, callable := function.Type.(*typeinfo.FuncType)
			if !function.IsPub || !callable {
				continue
			}
			if typechecker.CanAdaptFirstCallArgument(ctx, module, fnType.Params[0], baseType) {
				name := alias + "::" + function.Name
				items = appendOperationCompletion(items, seen, function, name, fnType, replacement, rewrite, pipe, preserveArguments)
			}
		}
	}
	return sortCompletionItems(items)
}

func operationFunctionsWithPrefix(functions []*symbols.Symbol, prefix string) []*symbols.Symbol {
	start := sort.Search(len(functions), func(i int) bool {
		return functions[i].Name >= prefix
	})
	end := start
	for end < len(functions) && strings.HasPrefix(functions[end].Name, prefix) {
		end++
	}
	return functions[start:end]
}

func appendOperationCompletion(items []CompletionItem, seen map[string]struct{}, sym *symbols.Symbol, name string, fnType *typeinfo.FuncType, replacement, rewrite Range, pipe, preserveArguments bool) []CompletionItem {
	if fnType == nil || len(fnType.Params) == 0 {
		return items
	}
	kind := completionItemKind(sym.Kind)
	key := fmt.Sprintf("%d|%s|%s", kind, name, typeinfo.TypeText(fnType))
	if _, duplicate := seen[key]; duplicate {
		return items
	}
	seen[key] = struct{}{}
	call := name
	if !preserveArguments {
		call = completionCallText(name, fnType)
	}
	editRange := replacement
	newText := call
	function := sym.Kind == symbols.SymbolFunc
	preferred := pipe == function
	if pipe && !function {
		editRange = rewrite
		newText = "." + call
	} else if !pipe && function {
		editRange = rewrite
		newText = " |> " + call
	}
	sortPrefix := "1"
	if preferred {
		sortPrefix = "0"
	}
	return append(items, CompletionItem{
		Label:            name,
		Kind:             kind,
		Detail:           renderSymbol(sym, symbolRenderContext{Name: name, Type: fnType}),
		SortText:         sortPrefix + name,
		InsertTextFormat: 2,
		TextEdit:         TextEdit{Range: editRange, NewText: newText},
	})
}

func completionCallText(name string, fnType *typeinfo.FuncType) string {
	var arguments []string
	for i := 1; i < len(fnType.Params); i++ {
		parameter := "arg"
		if i < len(fnType.ParamNames) && fnType.ParamNames[i] != "" {
			parameter = fnType.ParamNames[i]
		}
		arguments = append(arguments, fmt.Sprintf("${%d:%s}", i, parameter))
	}
	return name + "(" + strings.Join(arguments, ", ") + ")"
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
		Detail:   renderSymbol(sym, symbolRenderContext{}),
		TextEdit: TextEdit{Range: replacement, NewText: sym.Name},
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
		if cmp := strings.Compare(a.SortText, b.SortText); cmp != 0 && (a.SortText != "" || b.SortText != "") {
			return cmp
		}
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
	for range position.Line {
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
