//go:build !windows

package discovery

import "errors"

func Discover() (Result, error) {
	return Result{}, errors.New("installed game discovery is only available on Windows")
}
