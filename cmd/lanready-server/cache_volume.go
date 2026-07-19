package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const cacheVolumeSentinelName = ".lanready-cache-volume"

func verifyCacheVolume(root, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return nil
	}
	if len(expected) > 128 || strings.ContainsAny(expected, "\r\n") {
		return errors.New("cache volume ID is invalid")
	}
	path := filepath.Join(root, cacheVolumeSentinelName)
	entry, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("cache volume sentinel: %w", err)
	}
	if !entry.Mode().IsRegular() || entry.Mode()&os.ModeSymlink != 0 {
		return errors.New("cache volume sentinel must be a small regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read cache volume sentinel: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat cache volume sentinel: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(entry, opened) || opened.Size() > 256 {
		return errors.New("cache volume sentinel changed or is too large")
	}
	content, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil {
		return fmt.Errorf("read cache volume sentinel: %w", err)
	}
	if len(content) > 256 {
		return errors.New("cache volume sentinel is too large")
	}
	if strings.TrimSpace(string(content)) != expected {
		return errors.New("cache volume sentinel does not match configured volume ID")
	}
	return nil
}
