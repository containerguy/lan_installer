package protocol

import "testing"

func TestCompareSemanticVersions(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.2.0", -1},
		{"1.0.0", "0.99.99", 1},
		{"1.0.0-alpha.1", "1.0.0-alpha.2", -1},
		{"1.0.0-alpha", "1.0.0", -1},
		{"1.0.0-1", "1.0.0-alpha", -1},
	} {
		got, err := CompareSemanticVersions(test.left, test.right)
		if err != nil || got != test.want {
			t.Fatalf("compare %q %q: %d %v", test.left, test.right, got, err)
		}
	}
	for _, invalid := range []string{"1", "1.2", "01.2.3", "1.2.3+build", "1.2.3-01", "1.2.3-"} {
		if _, err := CompareSemanticVersions(invalid, "1.0.0"); err == nil {
			t.Fatalf("invalid version accepted: %q", invalid)
		}
	}
}
