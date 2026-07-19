package semver

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

type Version struct {
	Major int
	Minor int
	Patch int
}

func Parse(text string) (*Version, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "latest" || text == "*" {
		return nil, fmt.Errorf("invalid version %q", text)
	}
	matches := versionPattern.FindStringSubmatch(text)
	if matches == nil {
		return nil, fmt.Errorf("invalid version format %q", text)
	}
	parts := [3]int{}
	for i := range parts {
		value, err := strconv.Atoi(matches[i+1])
		if err != nil {
			return nil, fmt.Errorf("invalid version component %q: %w", matches[i+1], err)
		}
		parts[i] = value
	}
	return &Version{Major: parts[0], Minor: parts[1], Patch: parts[2]}, nil
}

func (v *Version) Compare(other *Version) int {
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}
	return 0
}

// Constraint syntax is a compatibility layer for current manifests. Go-style
// module selection and semantic import versioning are tracked in issue #58.
type constraintKind uint8

const (
	constraintAny constraintKind = iota
	constraintExact
	constraintGreaterEqual
	constraintGreater
	constraintLessEqual
	constraintLess
	constraintTilde
	constraintCaret
)

type constraint struct {
	kind      constraintKind
	reference *Version
}

func parseConstraint(text string) (constraint, error) {
	text = strings.TrimSpace(text)
	if text == "latest" || text == "*" {
		return constraint{kind: constraintAny}, nil
	}
	prefixes := []struct {
		text string
		kind constraintKind
	}{
		{text: ">=", kind: constraintGreaterEqual},
		{text: "<=", kind: constraintLessEqual},
		{text: ">", kind: constraintGreater},
		{text: "<", kind: constraintLess},
		{text: "=", kind: constraintExact},
		{text: "~", kind: constraintTilde},
		{text: "^", kind: constraintCaret},
	}
	for _, prefix := range prefixes {
		if after, ok := strings.CutPrefix(text, prefix.text); ok {
			reference, err := Parse(after)
			if err != nil {
				return constraint{}, err
			}
			return constraint{kind: prefix.kind, reference: reference}, nil
		}
	}
	reference, err := Parse(text)
	if err != nil {
		return constraint{}, err
	}
	return constraint{kind: constraintExact, reference: reference}, nil
}

func ValidateConstraint(text string) error {
	_, err := parseConstraint(text)
	return err
}

func Match(version, constraintText string) (bool, error) {
	parsedVersion, err := Parse(version)
	if err != nil {
		return false, err
	}
	parsedConstraint, err := parseConstraint(constraintText)
	if err != nil {
		return false, err
	}
	return parsedConstraint.matches(parsedVersion), nil
}

func (c constraint) matches(version *Version) bool {
	if c.kind == constraintAny {
		return true
	}
	comparison := version.Compare(c.reference)
	switch c.kind {
	case constraintExact:
		return comparison == 0
	case constraintGreaterEqual:
		return comparison >= 0
	case constraintGreater:
		return comparison > 0
	case constraintLessEqual:
		return comparison <= 0
	case constraintLess:
		return comparison < 0
	case constraintTilde:
		return comparison >= 0 && version.Major == c.reference.Major && version.Minor == c.reference.Minor
	case constraintCaret:
		if comparison < 0 || version.Major != c.reference.Major {
			return false
		}
		if c.reference.Major > 0 {
			return true
		}
		if version.Minor != c.reference.Minor {
			return false
		}
		return c.reference.Minor > 0 || version.Patch == c.reference.Patch
	default:
		return false
	}
}

func BestMatch(versions []string, constraintText string) (string, error) {
	parsedConstraint, err := parseConstraint(constraintText)
	if err != nil {
		return "", err
	}
	return bestMatchingVersion(versions, []constraint{parsedConstraint}, fmt.Sprintf("no version matches %q", constraintText))
}

func BestMatchAll(versions []string, constraintTexts []string) (string, error) {
	if len(constraintTexts) == 0 {
		constraintTexts = []string{"latest"}
	}
	constraints := make([]constraint, 0, len(constraintTexts))
	for _, text := range constraintTexts {
		parsed, err := parseConstraint(text)
		if err != nil {
			return "", err
		}
		constraints = append(constraints, parsed)
	}
	return bestMatchingVersion(versions, constraints, fmt.Sprintf("version conflict for constraints %v", constraintTexts))
}

func bestMatchingVersion(versions []string, constraints []constraint, noMatchMessage string) (string, error) {
	var best *Version
	bestTag := ""
	for _, candidate := range versions {
		parsed, err := Parse(candidate)
		if err != nil {
			continue
		}
		matched := true
		for _, constraint := range constraints {
			if !constraint.matches(parsed) {
				matched = false
				break
			}
		}
		if matched && (best == nil || parsed.Compare(best) > 0) {
			best = parsed
			bestTag = strings.TrimSpace(candidate)
		}
	}
	if best == nil {
		return "", errors.New(noMatchMessage)
	}
	return bestTag, nil
}
