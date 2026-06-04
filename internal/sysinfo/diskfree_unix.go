//go:build !windows

package sysinfo

import "syscall"

// DiskFreeGB returns free disk space in gigabytes for the filesystem
// containing path. Returns 0 on any error.
func DiskFreeGB(path string) float64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return float64(st.Bavail) * float64(st.Bsize) / (1 << 30)
}
