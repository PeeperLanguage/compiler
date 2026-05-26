package semmeta

import "testing"

func TestBindingFlags(t *testing.T) {
	var f BindingFlags
	if f.Mutable() {
		t.Fatalf("expected zero flags to be false")
	}
	f = FlagMutable
	if !f.Mutable() {
		t.Fatalf("expected mutable flag to be true")
	}
}

func TestParamFlags(t *testing.T) {
	var f ParamFlags
	if f.Variadic() {
		t.Fatalf("expected zero flags to be false")
	}
	f = FlagVariadic
	if !f.Variadic() {
		t.Fatalf("expected variadic flag to be true")
	}
}

func TestParamSpecDefaultFlag(t *testing.T) {
	spec := ParamSpec[int]{Name: "value", Type: 7, HasDefault: true}
	if !spec.HasDefault {
		t.Fatal("expected param spec to keep default metadata")
	}
}

func TestReceiverKeyString(t *testing.T) {
	key := ReceiverKey{Kind: ReceiverRefMut, TypeName: "Point"}
	if got := key.String(); got != "&mut Point" {
		t.Fatalf("unexpected key string: %q", got)
	}
}
