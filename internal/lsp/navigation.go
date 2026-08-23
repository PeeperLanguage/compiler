package lsp

import (
	"slices"

	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

func symLocationsMatch(l1, l2 *source.Location) bool {
	if l1 == nil || l2 == nil {
		return l1 == l2
	}
	if l1.Filename == nil || l2.Filename == nil {
		return l1.Filename == l2.Filename
	}
	if *l1.Filename != *l2.Filename {
		return false
	}
	if l1.Start == nil || l2.Start == nil || l1.End == nil || l2.End == nil {
		return false
	}
	return l1.Start.Line == l2.Start.Line && l1.Start.Column == l2.Start.Column
}

func (s *ServerState) HandleDefinition(params DefinitionParams) ([]Location, error) {
	filePath, err := uriToPath(string(params.TextDocument.URI))
	if err != nil {
		return nil, invalidParams(err.Error())
	}
	ctx, mod := s.currentCompiledModule(filePath)
	text, ok := sourceTextForFile(ctx, filePath)
	position, positionOK := sourcePositionAt(text, params.Position)
	if !ok || !positionOK {
		return nil, nil
	}
	cc := buildCursorContext(ctx, mod, position)
	if cc == nil {
		return nil, nil
	}
	ident, _ := cc.node.(*ast.Ident)
	if ident == nil {
		return nil, nil
	}
	sym := resolveIdentSymbol(ident, cc.parents, cc.module, cc.ctx)
	if sym == nil || sym.Location == nil || sym.Location.Start == nil || sym.Location.End == nil || sym.Location.Filename == nil {
		return nil, nil
	}

	targetText, ok := sourceTextForFile(ctx, *sym.Location.Filename)
	targetRange, rangeOK := rangeAtLocation(targetText, sym.Location)
	if !ok || !rangeOK {
		return nil, nil
	}
	return []Location{
		{
			URI:   DocumentURI(pathToURI(*sym.Location.Filename)),
			Range: targetRange,
		},
	}, nil
}

func (s *ServerState) HandleRename(params RenameParams) (*WorkspaceEdit, error) {
	if !token.IsValidSymbolName(params.NewName) {
		return nil, invalidParams("invalid rename target")
	}
	filePath, err := uriToPath(string(params.TextDocument.URI))
	if err != nil {
		return nil, invalidParams(err.Error())
	}
	ctx, mod := s.currentCompiledModule(filePath)
	text, ok := sourceTextForFile(ctx, filePath)
	position, positionOK := sourcePositionAt(text, params.Position)
	if !ok || !positionOK {
		return nil, nil
	}
	cc := buildCursorContext(ctx, mod, position)
	if cc == nil {
		return nil, nil
	}
	ident, _ := cc.node.(*ast.Ident)
	if ident == nil {
		return nil, nil
	}
	if isFixedReceiverReturnOrigin(ident, cc.parents[ident.ID()]) {
		return &WorkspaceEdit{Changes: map[DocumentURI][]TextEdit{}}, nil
	}
	targetSym := resolveIdentSymbol(ident, cc.parents, cc.module, cc.ctx)
	if targetSym == nil || targetSym.Location == nil {
		return nil, nil
	}

	changes := make(map[DocumentURI][]TextEdit)

	if s.LastCtx == nil {
		return nil, nil
	}

	for _, mod := range s.LastCtx.Modules() {
		if mod.AST == nil {
			continue
		}
		parents := cc.parents
		if mod != cc.module {
			parents = make(map[ast.NodeID]ast.Node)
		}
		moduleText, hasModuleText := sourceTextForFile(s.LastCtx, mod.FilePath)
		walkModuleAST(mod, func(n ast.Node, parent ast.Node) bool {
			if parent != nil {
				parents[n.ID()] = parent
			}
			ident, ok := n.(*ast.Ident)
			if !ok || ident.Name != targetSym.Name {
				return true
			}
			if isFixedReceiverReturnOrigin(ident, parents[ident.ID()]) {
				return true
			}
			resolved := resolveIdentSymbol(ident, parents, mod, s.LastCtx)
			loc := ast.LocOf(ident)
			if resolved == nil {
				if !symLocationsMatch(loc, targetSym.Location) {
					return true
				}
			} else if !symLocationsMatch(resolved.Location, targetSym.Location) && !symLocationsMatch(loc, targetSym.Location) {
				return true
			}
			uri := DocumentURI(pathToURI(mod.FilePath))
			if editRange, ok := rangeAtLocation(moduleText, loc); hasModuleText && ok {
				changes[uri] = append(changes[uri], TextEdit{
					Range:   editRange,
					NewText: params.NewName,
				})
			}
			return true
		})
	}

	return &WorkspaceEdit{Changes: changes}, nil
}

func isFixedReceiverReturnOrigin(ident *ast.Ident, parent ast.Node) bool {
	if ident == nil || ident.Name != "self" || parent == nil {
		return false
	}
	switch node := parent.(type) {
	case *ast.FnDecl:
		return node.Receiver != nil && returnOriginsContain(node.ReturnOrigins, ident)
	case *ast.InterfaceType:
		for _, method := range node.Methods {
			if method.Receiver != nil && returnOriginsContain(method.ReturnOrigins, ident) {
				return true
			}
		}
	}
	return false
}

func returnOriginsContain(clause *ast.ReturnOriginClause, ident *ast.Ident) bool {
	if clause == nil || ident == nil {
		return false
	}
	return slices.Contains(clause.Sources, ident)
}
