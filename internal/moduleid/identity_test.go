package moduleid

import "testing"

func TestIDStringFramesComponentsWithoutCollisions(t *testing.T) {
	first := ID{Origin: "local", Namespace: "ab", Dependency: "c", ImportPath: "sample/value"}
	second := ID{Origin: "local", Namespace: "a", Dependency: "bc", ImportPath: "sample/value"}
	if first.String() == second.String() {
		t.Fatalf("length-ambiguous module identities collide: %q", first.String())
	}
	if first.String() != first.String() {
		t.Fatal("module identity encoding is not deterministic")
	}
}

func TestIDDependsOnLogicalIdentityOnly(t *testing.T) {
	id := ID{Origin: "local", ImportPath: "app/math/counter"}
	if id.String() == "" || !id.Valid() {
		t.Fatalf("valid module identity = %#v", id)
	}
	if (ID{}).Valid() {
		t.Fatal("zero module identity accepted as valid")
	}
	if (ID{Origin: "local"}).Valid() {
		t.Fatal("module identity without import path accepted as valid")
	}
	if (ID{ImportPath: "app/math/counter"}).Valid() {
		t.Fatal("module identity without origin accepted as valid")
	}
}
