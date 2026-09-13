//go:build linux

package host

import "syscall"

func statfs(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize) // #nosec G115 -- block size is positive
	return st.Blocks * bs, st.Bavail * bs, nil
}
