package client

import "testing"

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	if _, err := safeJoin(root, "game/data.bin"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"../outside", "game/../../outside", "/absolute"} {
		if _, err := safeJoin(root, invalid); err == nil {
			t.Fatalf("unsafe path %q was accepted", invalid)
		}
	}
}
