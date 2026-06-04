//go:build windows

package sysinfo

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// DiskFreeGB returns free disk space in gigabytes for the filesystem
// containing path. Returns 0 on any error.
func DiskFreeGB(path string) float64 {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr,
		(*uint64)(unsafe.Pointer(&free)),
		(*uint64)(unsafe.Pointer(&total)),
		(*uint64)(unsafe.Pointer(&totalFree)),
	); err != nil {
		return 0
	}
	return float64(free) / (1 << 30)
}
