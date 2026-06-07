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

		// === BYTE RULES ===
		{"byte to i32", &IntegerType{Signed: true, Bits: 32}, &IntegerType{Signed: false, Bits: 8}, ExplicitCastable},
		{"i32 to byte", &IntegerType{Signed: false, Bits: 8}, &IntegerType{Signed: true, Bits: 32}, ExplicitCastable},
		{"byte to f64", &FloatType{Bits: 64}, &IntegerType{Signed: false, Bits: 8}, ExplicitCastable},
		{"byte to byte", &IntegerType{Signed: false, Bits: 8}, &IntegerType{Signed: false, Bits: 8}, Compatible},

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
		// Note: u8 (8-bit unsigned) is treated as byte, so it requires explicit cast
		{"u16 to u32", &IntegerType{Signed: false, Bits: 32}, &IntegerType{Signed: false, Bits: 16}, Compatible},
		{"u32 to u64", &IntegerType{Signed: false, Bits: 64}, &IntegerType{Signed: false, Bits: 32}, Compatible},

		// === UNSIGNED NARROWING ===
		{"u16 to u8", &IntegerType{Signed: false, Bits: 8}, &IntegerType{Signed: false, Bits: 16}, ExplicitCastable},
		{"u32 to u16", &IntegerType{Signed: false, Bits: 16}, &IntegerType{Signed: false, Bits: 32}, ExplicitCastable},

		// === SIGNED <-> UNSIGNED ===
		{"i32 to u32", &IntegerType{Signed: false, Bits: 32}, &IntegerType{Signed: true, Bits: 32}, ExplicitCastable},
		{"u32 to i32", &IntegerType{Signed: true, Bits: 32}, &IntegerType{Signed: false, Bits: 32}, ExplicitCastable},
		{"i8 to u16", &IntegerType{Signed: false, Bits: 16}, &IntegerType{Signed: true, Bits: 8}, ExplicitCastable},

		// === FLOAT WIDENING ===
		{"f32 to f64", &FloatType{Bits: 64}, &FloatType{Bits: 32}, Compatible},

		// === FLOAT NARROWING ===
		{"f64 to f32", &FloatType{Bits: 32}, &FloatType{Bits: 64}, ExplicitCastable},

		// === INTEGER TO FLOAT ===
		// f64 can represent all i32 and smaller exactly
		{"i8 to f64", &FloatType{Bits: 64}, &IntegerType{Signed: true, Bits: 8}, Compatible},
		{"i16 to f64", &FloatType{Bits: 64}, &IntegerType{Signed: true, Bits: 16}, Compatible},
		{"i32 to f64", &FloatType{Bits: 64}, &IntegerType{Signed: true, Bits: 32}, Compatible},
		{"u16 to f64", &FloatType{Bits: 64}, &IntegerType{Signed: false, Bits: 16}, Compatible},
		{"u32 to f64", &FloatType{Bits: 64}, &IntegerType{Signed: false, Bits: 32}, Compatible},

		// i32 to f32: NOT allowed (f32 mantissa is only 24 bits, can't represent all i32)
		{"i32 to f32", &FloatType{Bits: 32}, &IntegerType{Signed: true, Bits: 32}, ExplicitCastable},
		{"u32 to f32", &FloatType{Bits: 32}, &IntegerType{Signed: false, Bits: 32}, ExplicitCastable},

		// f32 can represent i16 and smaller exactly (24-bit mantissa)
		{"i8 to f32", &FloatType{Bits: 32}, &IntegerType{Signed: true, Bits: 8}, Compatible},
		{"i16 to f32", &FloatType{Bits: 32}, &IntegerType{Signed: true, Bits: 16}, Compatible},
		{"u16 to f32", &FloatType{Bits: 32}, &IntegerType{Signed: false, Bits: 16}, Compatible},

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
