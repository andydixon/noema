//go:build !linux

package host

import "errors"

func statfs(string) (uint64, uint64, error) {
	return 0, 0, errors.New("host.disk.usage is only implemented on Linux")
}
