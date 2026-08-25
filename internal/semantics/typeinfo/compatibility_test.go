package typeinfo

import (
	"testing"
)

func TestCheckNumericCompatibility(t *testing.T) {
	tests := []struct {
		name string
		dst  Type
		src  Type
		want Compatibility
	}{
		// === SAME TYPE ===
		{"same i32", &IntegerType{Signed: true, Bits: 32}, &IntegerType{Signed: true, Bits: 32}, Compatible},
		{"same f64", &FloatType{Bits: 64}, &FloatType{Bits: 64}, Compatible},
		{"same u8", &IntegerType{Signed: false, Bits: 8}, &IntegerType{Signed: false, Bits: 8}, Compatible},

		// === BYTE CLASS ===
		{"byte to i32", &IntegerType{Signed: true, Bits: 32}, &ByteType{}, ExplicitCastable},
		{"i32 to byte", &ByteType{}, &IntegerType{Signed: true, Bits: 32}, ExplicitCastable},
		{"byte to f64", &FloatType{Bits: 64}, &ByteType{}, ExplicitCastable},
		{"byte to byte", &ByteType{}, &ByteType{}, Compatible},
		{"u8 to byte", &ByteType{}, &IntegerType{Signed: false, Bits: 8}, ExplicitCastable},

		// === INTEGER WIDENING (same signedness) ===
		{"i8 to i16", &IntegerType{Signed: true, Bits: 16}, &IntegerType{Signed: true, Bits: 8}, Compatible},
		{"i8 to i32", &IntegerType{Signed: true, Bits: 32}, &IntegerType{Signed: true, Bits: 8}, Compatible},
		{"i16 to i32", &IntegerType{Signed: true, Bits: 32}, &IntegerType{Signed: true, Bits: 16}, Compatible},
		{"i32 to i64", &IntegerType{Signed: true, Bits: 64}, &IntegerType{Signed: true, Bits: 32}, Compatible},

		// === INTEGER NARROWING (same signedness) ===
		{"i16 to i8", &IntegerType{Signed: true, Bits: 8}, &IntegerType{Signed: true, Bits: 16}, ExplicitCastable},
		{"i32 to i16", &IntegerType{Signed: true, Bits: 16}, &IntegerType{Signed: true, Bits: 32}, ExplicitCastable},
		{"i64 to i32", &IntegerType{Signed: true, Bits: 32}, &IntegerType{Signed: true, Bits: 64}, ExplicitCastable},

		// === UNSIGNED WIDENING ===
		{"u8 to u16", &IntegerType{Signed: false, Bits: 16}, &IntegerType{Signed: false, Bits: 8}, Compatible},
		{"u16 to u32", &IntegerType{Signed: false, Bits: 32}, &IntegerType{Signed: false, Bits: 16}, Compatible},
		{"u32 to u64", &IntegerType{Signed: false, Bits: 64}, &IntegerType{Signed: false, Bits: 32}, Compatible},

		// === UNSIGNED NARROWING ===
		{"u16 to u8", &IntegerType{Signed: false, Bits: 8}, &IntegerType{Signed: false, Bits: 16}, ExplicitCastable},
		{"u32 to u16", &IntegerType{Signed: false, Bits: 16}, &IntegerType{Signed: false, Bits: 32}, ExplicitCastable},

		// === SIGNED <-> UNSIGNED ===
		{"i32 to u32", &IntegerType{Signed: false, Bits: 32}, &IntegerType{Signed: true, Bits: 32}, ExplicitCastable},
		{"u32 to i32", &IntegerType{Signed: true, Bits: 32}, &IntegerType{Signed: false, Bits: 32}, ExplicitCastable},
		{"i8 to u16", &IntegerType{Signed: false, Bits: 16}, &IntegerType{Signed: true, Bits: 8}, Compatible},
		{"u8 to i16", &IntegerType{Signed: true, Bits: 16}, &IntegerType{Signed: false, Bits: 8}, Compatible},

		// === FLOAT WIDENING ===
		{"f32 to f64", &FloatType{Bits: 64}, &FloatType{Bits: 32}, Compatible},

		// === FLOAT NARROWING ===
		{"f64 to f32", &FloatType{Bits: 32}, &FloatType{Bits: 64}, ExplicitCastable},

		// === INTEGER TO FLOAT: CROSS-CLASS ===
		{"i8 to f64", &FloatType{Bits: 64}, &IntegerType{Signed: true, Bits: 8}, ExplicitCastable},
		{"i16 to f64", &FloatType{Bits: 64}, &IntegerType{Signed: true, Bits: 16}, ExplicitCastable},
		{"i32 to f64", &FloatType{Bits: 64}, &IntegerType{Signed: true, Bits: 32}, ExplicitCastable},
		{"u16 to f64", &FloatType{Bits: 64}, &IntegerType{Signed: false, Bits: 16}, ExplicitCastable},
		{"u32 to f64", &FloatType{Bits: 64}, &IntegerType{Signed: false, Bits: 32}, ExplicitCastable},
		{"i32 to f32", &FloatType{Bits: 32}, &IntegerType{Signed: true, Bits: 32}, ExplicitCastable},
		{"u32 to f32", &FloatType{Bits: 32}, &IntegerType{Signed: false, Bits: 32}, ExplicitCastable},
		{"i8 to f32", &FloatType{Bits: 32}, &IntegerType{Signed: true, Bits: 8}, ExplicitCastable},
		{"i16 to f32", &FloatType{Bits: 32}, &IntegerType{Signed: true, Bits: 16}, ExplicitCastable},
		{"u16 to f32", &FloatType{Bits: 32}, &IntegerType{Signed: false, Bits: 16}, ExplicitCastable},

		// === FLOAT TO INTEGER ===
		// Always explicit (fractional part loss)
		{"f32 to i32", &IntegerType{Signed: true, Bits: 32}, &FloatType{Bits: 32}, ExplicitCastable},
		{"f64 to i32", &IntegerType{Signed: true, Bits: 32}, &FloatType{Bits: 64}, ExplicitCastable},
		{"f32 to u32", &IntegerType{Signed: false, Bits: 32}, &FloatType{Bits: 32}, ExplicitCastable},

		// === NON-NUMERIC ===
		{"bool to i32", &IntegerType{Signed: true, Bits: 32}, &BoolType{}, Incompatible},
		{"i32 to bool", &BoolType{}, &IntegerType{Signed: true, Bits: 32}, Incompatible},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckNumericCompatibility(tt.dst, tt.src)
			if got != tt.want {
				t.Errorf("CheckNumericCompatibility(%v, %v) = %v, want %v",
					tt.dst, tt.src, got, tt.want)
			}
		})
	}
}

func TestCompatibilityString(t *testing.T) {
	tests := []struct {
		compat Compatibility
		want   string
	}{
		{Compatible, "compatible"},
		{ExplicitCastable, "explicit_castable"},
		{Incompatible, "incompatible"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.compat.String(); got != tt.want {
				t.Errorf("Compatibility.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOptionalArrayAndReferenceCompatibility(t *testing.T) {
	if got := CheckCompatibility(&OptionalType{Inner: &IntegerType{Signed: true, Bits: 32}}, &NoneType{}); got != Compatible {
		t.Fatalf("optional none compat = %v, want compatible", got)
	}
	if got := CheckCompatibility(&OptionalType{Inner: &IntegerType{Signed: true, Bits: 32}}, &IntegerType{Signed: true, Bits: 32}); got != Compatible {
		t.Fatalf("optional inner compat = %v, want compatible", got)
	}
	if got := CheckCompatibility(&ArrayType{Len: "4", Elem: &IntegerType{Signed: true, Bits: 32}}, &ArrayType{Len: "4", Elem: &IntegerType{Signed: true, Bits: 32}}); got != Compatible {
		t.Fatalf("array compat = %v, want compatible", got)
	}
	if got := CheckCompatibility(&ArrayType{Shape: ArrayOwner, Elem: &StringType{}}, &ArrayType{Shape: ArrayOwner, Elem: &StringType{}}); got != Compatible {
		t.Fatalf("dynamic array compat = %v, want compatible", got)
	}
	if got := CheckCompatibility(
		&RefType{Target: &ArrayType{Shape: ArraySlice, Elem: &StringType{}}},
		&RefType{Target: &ArrayType{Shape: ArraySlice, Elem: &StringType{}}},
	); got != Compatible {
		t.Fatalf("slice-view ref compat = %v, want compatible", got)
	}
	shared := &RefType{Target: &IntegerType{Signed: true, Bits: 32}}
	mutable := &RefType{Mutable: true, Target: &IntegerType{Signed: true, Bits: 32}}
	if got := CheckCompatibility(shared, mutable); got != Compatible {
		t.Fatalf("mutable-to-shared ref compat = %v, want compatible", got)
	}
	if got := CheckCompatibility(mutable, shared); got != Incompatible {
		t.Fatalf("shared-to-mutable ref compat = %v, want incompatible", got)
	}
}

func TestOptionalCompatibilityAllowsOneLayerPromotion(t *testing.T) {
	i32 := &IntegerType{Signed: true, Bits: 32}
	inner := &OptionalType{Inner: i32}
	outer := &OptionalType{Inner: inner}
	if CheckCompatibility(outer, inner) != Compatible {
		t.Fatal("?T must promote into ??T as one intact payload layer")
	}
	if CheckCompatibility(inner, inner) != Compatible {
		t.Fatal("exact optional carrier assignment must remain compatible")
	}
}
