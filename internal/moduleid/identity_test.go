package moduleid

import "testing"

func TestIDStringFramesComponentsWithoutCollisions(t *testing.T) {
	first := ID{Origin: "local", Namespace: "ab", Dependency: "c", ImportPath: "sample/value"}
	second := ID{Origin: "local", Namespace: "a", Dependency: "bc", ImportPath: "sample/value"}
	if first.String() == second.String() {
		t.Fatalf("length-ambiguous module identities collide: %q", first.String())
	}
}

func TestIDDependsOnLogicalIdentityOnly(t *testing.T) {
	id := ID{Origin: "local", ImportPath: "app/math/counter"}
	if id.String() == "" || !id.IsValid() {
		t.Fatalf("valid module identity = %#v", id)
	}
	if (ID{}).IsValid() {
		t.Fatal("zero module identity accepted as valid")
	}
	if (ID{Origin: "local"}).IsValid() {
		t.Fatal("module identity without import path accepted as valid")
	}
	if (ID{ImportPath: "app/math/counter"}).IsValid() {
		t.Fatal("module identity without origin accepted as valid")
	}
}

func TestFunctionIdentityUsesModuleAndDeclarationSurface(t *testing.T) {
	owner := ID{Origin: "local", ImportPath: "app/math"}
	first := FunctionIdentity(owner, "fn:main:()->i32", 0)
	second := FunctionIdentity(owner, "fn:main:()->i32", 0)
	if first == "" || first != second {
		t.Fatalf("same declaration identity = %q and %q", first, second)
	}
	if first == FunctionIdentity(owner, "fn:main:()->i32", 1) {
		t.Fatal("same-surface redeclarations share function identity")
	}
	if first == FunctionIdentity(owner, "fn:main:()->i64", 0) {
		t.Fatal("different declaration surfaces share function identity")
	}
	if first == FunctionIdentity(ID{Origin: "local", ImportPath: "app/other"}, "fn:main:()->i32", 0) {
		t.Fatal("different modules share function identity")
	}
	if FunctionIdentity(ID{}, "fn:main:()->i32", 0) != "" {
		t.Fatal("invalid module produced function identity")
	}
	if FunctionIdentity(owner, "fn:main:()->i32", -1) != "" {
		t.Fatal("negative declaration occurrence produced function identity")
	}
}
