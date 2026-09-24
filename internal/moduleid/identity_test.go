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
