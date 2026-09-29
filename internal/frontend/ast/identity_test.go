package ast

import (
	"testing"

	"compiler/internal/moduleid"
	"compiler/internal/source"
)

func TestPublishFunctionIdentitiesKeepsUnchangedFunctionNodesStable(t *testing.T) {
	owner := moduleid.ID{Origin: "local", ImportPath: "app/main"}
	function := func(parsedID source.NodeID) *FnDecl {
		return &FnDecl{
			NodeIDHolder: NodeIDHolder{NodeID: parsedID},
			Documented:   Documented{DeclSurface: "fn:main:()->i32"},
			Name:         &Ident{NodeIDHolder: NodeIDHolder{NodeID: parsedID}},
			Body:         &BlockStmt{NodeIDHolder: NodeIDHolder{NodeID: parsedID}},
		}
	}
	first := function(source.ParsedNodeID(10))
	second := function(source.ParsedNodeID(40))
	PublishFunctionIdentities(owner, &Module{Stmts: []Stmt{first}})
	PublishFunctionIdentities(owner, &Module{Stmts: []Stmt{second}})
	firstFunctionID := first.ID().Function()
	secondFunctionID := second.ID().Function()
	if firstFunctionID == "" || firstFunctionID != secondFunctionID {
		t.Fatalf("function identities = %q and %q", firstFunctionID, secondFunctionID)
	}
	firstNodes := []source.NodeID{first.ID(), first.Name.ID(), first.Body.ID()}
	secondNodes := []source.NodeID{second.ID(), second.Name.ID(), second.Body.ID()}
	for index := range firstNodes {
		if firstNodes[index] != secondNodes[index] || firstNodes[index].Function() != firstFunctionID {
			t.Fatalf("node %d identities = %v and %v", index, firstNodes[index], secondNodes[index])
		}
	}
}

func TestPublishFunctionIdentitiesDisambiguatesRecoveryRedeclarations(t *testing.T) {
	owner := moduleid.ID{Origin: "local", ImportPath: "app/main"}
	first := &FnDecl{Documented: Documented{DeclSurface: "fn:main:()"}}
	second := &FnDecl{Documented: Documented{DeclSurface: "fn:main:()"}}
	module := &Module{Stmts: []Stmt{first, second}}
	PublishFunctionIdentities(owner, module)
	firstFunctionID := first.ID().Function()
	secondFunctionID := second.ID().Function()
	if firstFunctionID == "" || secondFunctionID == "" || firstFunctionID == secondFunctionID {
		t.Fatalf("recovery identities = %q and %q", firstFunctionID, secondFunctionID)
	}
}
