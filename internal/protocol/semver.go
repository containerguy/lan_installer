package protocol

import (
	"errors"
	"strings"

	"golang.org/x/mod/semver"
)

// CompareSemanticVersions compares two strict "major.minor.patch[-prerelease]"
// version strings (no "v" prefix, no build metadata) per Semantic Versioning
// 2.0.0 precedence rules.
func CompareSemanticVersions(left, right string) (int, error) {
	a, err := canonicalSemanticVersion(left)
	if err != nil {
		return 0, err
	}
	b, err := canonicalSemanticVersion(right)
	if err != nil {
		return 0, err
	}
	return semver.Compare(a, b), nil
}

func canonicalSemanticVersion(value string) (string, error) {
	if strings.Contains(value, "+") {
		return "", errors.New("semantic version build metadata is not supported")
	}
	core, _, _ := strings.Cut(value, "-")
	if strings.Count(core, ".") != 2 {
		return "", errors.New("semantic version requires major.minor.patch")
	}
	canonical := "v" + value
	if !semver.IsValid(canonical) {
		return "", errors.New("semantic version is invalid")
	}
	return canonical, nil
}
