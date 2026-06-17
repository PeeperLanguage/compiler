package ast

// Inspect traverses the AST in depth-first order: it starts by calling f(node);
// if f returns true, Inspect invokes f recursively for each of the non-nil children of node,
// followed by a call to f(nil).
func Inspect(node Node, f func(Node) bool) {
	if node == nil || IsNilNode(node) {
		return
	}
	if !f(node) {
		return
	}

	switch n := node.(type) {
	case *ImportDecl:
		Inspect(n.Path, f)
		Inspect(n.Alias, f)
	case *FnDecl:
		Inspect(n.Name, f)
		for _, tp := range n.TypeParams {
			Inspect(tp.Name, f)
		}
		for _, p := range n.Params {
			Inspect(p.Name, f)
			Inspect(p.Type, f)
		}
		Inspect(n.ReturnType, f)
		Inspect(n.Body, f)
	case *LetDecl:
		Inspect(n.Name, f)
		Inspect(n.Type, f)
		Inspect(n.Value, f)
	case *ConstDecl:
		Inspect(n.Name, f)
		Inspect(n.Type, f)
		Inspect(n.Value, f)
	case *TypeAliasDecl:
		Inspect(n.Name, f)
		for _, tp := range n.TypeParams {
			Inspect(tp.Name, f)
		}
		Inspect(n.Type, f)
	case *StructDecl:
		Inspect(n.Name, f)
		for _, tp := range n.TypeParams {
			Inspect(tp.Name, f)
		}
		for _, field := range n.Fields {
			Inspect(field.Name, f)
			Inspect(field.Type, f)
		}
	case *InterfaceDecl:
		Inspect(n.Name, f)
		for _, tp := range n.TypeParams {
			Inspect(tp.Name, f)
		}
		for _, method := range n.Methods {
			Inspect(method.Name, f)
			for _, tp := range method.TypeParams {
				Inspect(tp.Name, f)
			}
			for _, p := range method.Params {
				Inspect(p.Name, f)
				Inspect(p.Type, f)
			}
			Inspect(method.ReturnType, f)
		}
	case *EnumDecl:
		Inspect(n.Name, f)
		for _, tp := range n.TypeParams {
			Inspect(tp.Name, f)
		}
		for _, v := range n.Variants {
			Inspect(v.Name, f)
		}
	case *ImplDecl:
		Inspect(n.Target, f)
		for _, method := range n.Methods {
			Inspect(method, f)
		}
	case *BlockStmt:
		for _, stmt := range n.Stmts {
			Inspect(stmt, f)
		}
	case *ReturnStmt:
		Inspect(n.Value, f)
	case *IfStmt:
		Inspect(n.Cond, f)
		Inspect(n.Then, f)
		Inspect(n.Else, f)
	case *ExprStmt:
		Inspect(n.Expr, f)
	case *AssignStmt:
		Inspect(n.Target, f)
		Inspect(n.Value, f)
	case *Ident:
		// Leaf
	case *ScopeResolution:
		Inspect(n.Module, f)
		Inspect(n.Name, f)
	case *SelectorExpr:
		Inspect(n.Expr, f)
		Inspect(n.Name, f)
	case *StructLit:
		Inspect(n.Type, f)
		for _, field := range n.Fields {
			Inspect(field.Name, f)
			Inspect(field.Value, f)
		}
	case *UnaryExpr:
		Inspect(n.Expr, f)
	case *BinaryExpr:
		Inspect(n.Left, f)
		Inspect(n.Right, f)
	case *CallExpr:
		Inspect(n.Callee, f)
		for _, arg := range n.Args {
			Inspect(arg, f)
		}
	case *AsExpr:
		Inspect(n.Expr, f)
		Inspect(n.TypeExpr, f)
	case *RawPtrType:
		Inspect(n.Target, f)
	case *FuncType:
		for _, p := range n.Params {
			Inspect(p, f)
		}
		Inspect(n.Return, f)
	case *StructType:
		for _, field := range n.Fields {
			Inspect(field.Name, f)
			Inspect(field.Type, f)
		}
	case *InterfaceType:
		for _, method := range n.Methods {
			Inspect(method.Name, f)
			for _, tp := range method.TypeParams {
				Inspect(tp.Name, f)
			}
			for _, p := range method.Params {
				Inspect(p.Name, f)
				Inspect(p.Type, f)
			}
			Inspect(method.ReturnType, f)
		}
	case *EnumType:
		for _, v := range n.Variants {
			Inspect(v.Name, f)
		}
	case *BadExpr:
		// Leaf — no children
	case *BadStmt:
		// Leaf — no children
	case *BadDecl:
		// Leaf — no children
	}

	f(nil)
}
