package numeric

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

const (
	MaxIntegerBits = 1 << 23

	HexDigits = `[0-9a-fA-F]`
	HexNumber = `0[xX]` + HexDigits + `(?:` + HexDigits + `|_` + HexDigits + `)*`
	HexToken  = `0[xX][0-9A-Za-z](?:[0-9A-Za-z]|_[0-9A-Za-z])*`

	OctDigits = `[0-7]`
	OctNumber = `0[oO]` + OctDigits + `(?:` + OctDigits + `|_` + OctDigits + `)*`
	OctToken  = `0[oO][0-9A-Za-z](?:[0-9A-Za-z]|_[0-9A-Za-z])*`

	BinDigits = `[01]`
	BinNumber = `0[bB]` + BinDigits + `(?:` + BinDigits + `|_` + BinDigits + `)*`
	BinToken  = `0[bB][0-9A-Za-z](?:[0-9A-Za-z]|_[0-9A-Za-z])*`

	DecDigits = `[0-9]`
	DecNumber = DecDigits + `(?:` + DecDigits + `|_` + DecDigits + `)*`

	FloatFrac          = `\.` + DecDigits + `(?:` + DecDigits + `|_` + DecDigits + `)*`
	FloatExp           = `[eE][+-]?` + DecDigits + `(?:` + DecDigits + `|_` + DecDigits + `)*`
	FloatNumber        = DecNumber + `(?:` + FloatFrac + `)?(?:` + FloatExp + `)?`
	NumberSuffix       = `[iuf](?:[0-9_]+)?`
	NumberPattern      = `(?:` + HexNumber + `|` + OctNumber + `|` + BinNumber + `|` + FloatNumber + `)(?:` + NumberSuffix + `)?`
	NumberTokenPattern = `(?:` + HexToken + `|` + OctToken + `|` + BinToken + `|` + FloatNumber + `)(?:` + NumberSuffix + `)?`
)

var (
	decimalRegex    = regexp.MustCompile(`^-?` + DecNumber + `$`)
	hexRegex        = regexp.MustCompile(`^` + HexNumber + `$`)
	octalRegex      = regexp.MustCompile(`^` + OctNumber + `$`)
	binaryRegex     = regexp.MustCompile(`^` + BinNumber + `$`)
	floatRegex      = regexp.MustCompile(`^-?` + DecNumber + `\.` + DecDigits + `(?:` + DecDigits + `|_` + DecDigits + `)*$`)
	scientificRegex = regexp.MustCompile(`^-?` + DecNumber + `(?:\.` + DecDigits + `(?:` + DecDigits + `|_` + DecDigits + `)*)?` + FloatExp + `$`)
	numberRegex     = regexp.MustCompile(`^(?:` + HexNumber + `|` + OctNumber + `|` + BinNumber + `|` + FloatNumber + `)$`)
	suffixRegex     = regexp.MustCompile(`([iuf][0-9_]*)$`)
)

type Literal struct {
	Value        string
	ExplicitType string
}

func IsDecimal(s string) bool {
	return decimalRegex.MatchString(s)
}

func IsHexadecimal(s string) bool {
	return hexRegex.MatchString(s)
}

func IsOctal(s string) bool {
	return octalRegex.MatchString(s)
}

func IsBinary(s string) bool {
	return binaryRegex.MatchString(s)
}

func IsFloat(s string) bool {
	return floatRegex.MatchString(s) || scientificRegex.MatchString(s)
}

func IsValidNumber(s string) bool {
	return numberRegex.MatchString(s)
}

func CleanNumberString(s string) string {
	return strings.ReplaceAll(s, "_", "")
}

func LooksFloatLike(s string) bool {
	clean := CleanNumberString(s)
	if clean == "" {
		return false
	}
	lower := strings.ToLower(clean)
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0o") || strings.HasPrefix(lower, "0b") {
		return false
	}
	return strings.ContainsAny(clean, ".eE")
}

// ParseLiteral owns the boundary between source spelling and the numeric value
// consumed by semantic and IR phases. Type postfixes must never leak into
// constant parsing or backend text.
func ParseLiteral(raw string) (Literal, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return Literal{}, fmt.Errorf("invalid numeric literal %s", raw)
	}

	body := text
	suffix := ""
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0o") || strings.HasPrefix(lower, "0b") {
		if index := strings.IndexAny(lower[2:], "iu"); index >= 0 {
			index += 2
			body, suffix = text[:index], text[index:]
		}
	} else if match := suffixRegex.FindStringSubmatchIndex(text); match != nil {
		body = text[:match[2]]
		suffix = text[match[2]:match[3]]
	}

	if err := ValidateLiteral(body); err != nil {
		return Literal{}, err
	}
	if suffix != "" {
		if strings.Contains(suffix, "_") || len(suffix) == 1 {
			return Literal{}, fmt.Errorf("invalid numeric literal suffix %s", suffix)
		}
		if suffix[0] != 'f' && LooksFloatLike(body) {
			return Literal{}, fmt.Errorf("integer suffix %s cannot be used on a float literal", suffix)
		}
	}
	return Literal{Value: CleanNumberString(body), ExplicitType: suffix}, nil
}

func ValidateLiteral(s string) error {
	clean := CleanNumberString(s)
	if clean == "" {
		return fmt.Errorf("invalid numeric literal %s", s)
	}
	if IsValidNumber(clean) {
		return nil
	}
	lower := strings.ToLower(clean)
	switch {
	case strings.HasPrefix(lower, "0x"):
		return fmt.Errorf("invalid hexadecimal literal %s", s)
	case strings.HasPrefix(lower, "0o"):
		return fmt.Errorf("invalid octal literal %s", s)
	case strings.HasPrefix(lower, "0b"):
		return fmt.Errorf("invalid binary literal %s", s)
	case LooksFloatLike(clean):
		return fmt.Errorf("invalid float literal %s", s)
	default:
		return fmt.Errorf("invalid integer literal %s", s)
	}
}

func StringToBigInt(s string) (*big.Int, error) {
	clean := CleanNumberString(s)
	if clean == "" {
		return nil, fmt.Errorf("empty integer literal")
	}
	negative := false
	if clean[0] == '-' {
		negative = true
		clean = clean[1:]
	}

	base := 10
	switch {
	case strings.HasPrefix(clean, "0x") || strings.HasPrefix(clean, "0X"):
		base = 16
		clean = clean[2:]
	case strings.HasPrefix(clean, "0o") || strings.HasPrefix(clean, "0O"):
		base = 8
		clean = clean[2:]
	case strings.HasPrefix(clean, "0b") || strings.HasPrefix(clean, "0B"):
		base = 2
		clean = clean[2:]
	}

	value, ok := new(big.Int).SetString(clean, base)
	if !ok {
		return nil, fmt.Errorf("invalid integer literal: %s", s)
	}
	if negative {
		value.Neg(value)
	}
	return value, nil
}

func StringToFloat(s string) (float64, error) {
	clean := CleanNumberString(s)
	return strconv.ParseFloat(clean, 64)
}

func CanonicalizeIntegerLiteral(s string) (string, error) {
	value, err := StringToBigInt(s)
	if err != nil {
		return "", err
	}
	return value.String(), nil
}

func FitsIntegerLiteral(raw string, bitSize int, signed bool) bool {
	value, err := StringToBigInt(raw)
	if err != nil {
		return false
	}
	if signed {
		max := new(big.Int).Lsh(big.NewInt(1), uint(bitSize-1))
		min := new(big.Int).Neg(max)
		max.Sub(max, big.NewInt(1))
		return value.Cmp(min) >= 0 && value.Cmp(max) <= 0
	}
	if value.Sign() < 0 {
		return false
	}
	max := new(big.Int).Lsh(big.NewInt(1), uint(bitSize))
	max.Sub(max, big.NewInt(1))
	return value.Cmp(max) <= 0
}

func FitsFloatLiteral(raw string, bitSize int) bool {
	value, err := StringToFloat(raw)
	if err != nil {
		return false
	}
	switch bitSize {
	case 32:
		f32 := float32(value)
		return !math.IsInf(float64(f32), 0) && !math.IsNaN(float64(f32))
	case 64:
		return !math.IsInf(value, 0) && !math.IsNaN(value)
	default:
		return false
	}
}

func FitsIntegerLiteralInFloat(raw string, bitSize int) bool {
	value, err := StringToBigInt(raw)
	if err != nil {
		return false
	}
	floatValue := new(big.Float).SetInt(value)
	switch bitSize {
	case 32:
		f32, _ := floatValue.Float32()
		return !math.IsInf(float64(f32), 0) && !math.IsNaN(float64(f32))
	case 64:
		f64, _ := floatValue.Float64()
		return !math.IsInf(f64, 0) && !math.IsNaN(f64)
	default:
		return false
	}
}
