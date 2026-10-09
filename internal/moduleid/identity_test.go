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

func TestParseTypeIdentityReversesTypeIdentity(t *testing.T) {
	owner := ID{Origin: "core", Namespace: "core", ImportPath: "global"}
	gotOwner, gotName, ok := ParseTypeIdentity(TypeIdentity(owner, "Str"))
	if !ok || gotOwner != owner || gotName != "Str" {
		t.Fatalf("ParseTypeIdentity = %#v, %q, %v", gotOwner, gotName, ok)
	}
	instance := TypeIdentity(owner, "Box") + "<" + Frame("integer", "i32") + ">"
	if _, name, ok := ParseTypeIdentity(instance); !ok || name != "Box" {
		t.Fatalf("instance name = %q, %v; want Box", name, ok)
	}
	for _, identity := range []string{"", "Counter", "5_6c6f63616c_::Counter", TypeIdentity(owner, "")} {
		if _, _, ok := ParseTypeIdentity(identity); ok {
			t.Fatalf("ParseTypeIdentity(%q) accepted a non-module identity", identity)
		}
	}
}

func TestDisplayTextSkipsEmptyPartsAndEscapesSeparators(t *testing.T) {
	tests := []struct {
		id   ID
		want string
	}{
		{ID{Origin: "local", ImportPath: "app/alpha"}, "local:app/alpha"},
		{ID{Origin: "core", Namespace: "core", ImportPath: "global"}, "core:core@global"},
		{ID{Origin: "dependency", Namespace: "ns", Dependency: "dep", ImportPath: "a/b"}, "dependency:ns@dep+a/b"},
		{ID{Origin: "local", ImportPath: "app/my:dir/a b"}, "local:app/my%3Adir/a%20b"},
	}
	for _, test := range tests {
		if got := test.id.DisplayText(); got != test.want {
			t.Fatalf("DisplayText(%#v) = %q, want %q", test.id, got, test.want)
		}
	}
}

func TestDisplayTextKeepsDistinctIdentitiesDistinct(t *testing.T) {
	ids := []ID{
		{Origin: "local", ImportPath: "a/b"},
		{Origin: "local", Namespace: "a", ImportPath: "b"},
		{Origin: "local", Dependency: "a", ImportPath: "b"},
		{Origin: "local", ImportPath: "a@b"},
		{Origin: "local", ImportPath: "a+b"},
		{Origin: "local", ImportPath: "a%2Fb"},
		{Origin: "local", ImportPath: "a:b"},
		{Origin: "local:a", ImportPath: "b"},
	}
	seen := make(map[string]ID)
	for _, id := range ids {
		text := id.DisplayText()
		if previous, ok := seen[text]; ok {
			t.Fatalf("%#v and %#v share display text %q", previous, id, text)
		}
		seen[text] = id
	}
}
