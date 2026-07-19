package client

import (
	"fmt"
	"path/filepath"
	"strings"
)

func safeJoin(root, relative string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("target root is empty")
	}
	clean := filepath.Clean(relative)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe relative path %q", relative)
	}
	joined := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q leaves target root", relative)
	}
	return joined, nil
}
