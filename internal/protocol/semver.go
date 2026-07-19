package protocol

import (
	"errors"
	"strconv"
	"strings"
)

type semanticVersion struct {
	core       [3]uint64
	prerelease []string
}

func CompareSemanticVersions(left, right string) (int, error) {
	a, err := parseSemanticVersion(left)
	if err != nil {
		return 0, err
	}
	b, err := parseSemanticVersion(right)
	if err != nil {
		return 0, err
	}
	for index := range a.core {
		if a.core[index] < b.core[index] {
			return -1, nil
		}
		if a.core[index] > b.core[index] {
			return 1, nil
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) == 0 {
		return 0, nil
	}
	if len(a.prerelease) == 0 {
		return 1, nil
	}
	if len(b.prerelease) == 0 {
		return -1, nil
	}
	for index := 0; index < len(a.prerelease) && index < len(b.prerelease); index++ {
		leftPart, rightPart := a.prerelease[index], b.prerelease[index]
		leftNumber, leftNumeric := numericIdentifier(leftPart)
		rightNumber, rightNumeric := numericIdentifier(rightPart)
		switch {
		case leftNumeric && rightNumeric && leftNumber < rightNumber:
			return -1, nil
		case leftNumeric && rightNumeric && leftNumber > rightNumber:
			return 1, nil
		case leftNumeric && !rightNumeric:
			return -1, nil
		case !leftNumeric && rightNumeric:
			return 1, nil
		case leftPart < rightPart:
			return -1, nil
		case leftPart > rightPart:
			return 1, nil
		}
	}
	if len(a.prerelease) < len(b.prerelease) {
		return -1, nil
	}
	if len(a.prerelease) > len(b.prerelease) {
		return 1, nil
	}
	return 0, nil
}

func parseSemanticVersion(value string) (semanticVersion, error) {
	var version semanticVersion
	if strings.Contains(value, "+") {
		return version, errors.New("semantic version build metadata is not supported")
	}
	core, prerelease, hasPrerelease := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version, errors.New("semantic version requires major.minor.patch")
	}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return version, errors.New("semantic version core is invalid")
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return version, errors.New("semantic version core is invalid")
		}
		version.core[index] = number
	}
	if hasPrerelease {
		version.prerelease = strings.Split(prerelease, ".")
		for _, part := range version.prerelease {
			if part == "" || !validPrereleaseIdentifier(part) || (len(part) > 1 && part[0] == '0' && allDigits(part)) {
				return semanticVersion{}, errors.New("semantic version prerelease is invalid")
			}
		}
	}
	return version, nil
}

func validPrereleaseIdentifier(value string) bool {
	for _, character := range value {
		if character != '-' && (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

func numericIdentifier(value string) (uint64, bool) {
	if !allDigits(value) {
		return 0, false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return number, err == nil
}

func allDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}
