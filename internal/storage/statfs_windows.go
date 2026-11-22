//go:build windows

package storage

import (
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func getFSFreeMB(checkPath string) (int64, error) {
	pathPtr, err := windows.UTF16PtrFromString(checkPath)
	if err != nil {
		return 0, err
	}
	var freeBytesAvailable, totalBytes, totalFreeBytes int64
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")
	r1, _, errCall := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r1 == 0 {
		parent := filepath.Dir(checkPath)
		parentPtr, pErr := windows.UTF16PtrFromString(parent)
		if pErr != nil {
			return 0, errCall
		}
		r2, _, errCall2 := getDiskFreeSpaceEx.Call(
			uintptr(unsafe.Pointer(parentPtr)),
			uintptr(unsafe.Pointer(&freeBytesAvailable)),
			uintptr(unsafe.Pointer(&totalBytes)),
			uintptr(unsafe.Pointer(&totalFreeBytes)),
		)
		if r2 == 0 {
			return 0, errCall2
		}
	}
	return freeBytesAvailable / (1024 * 1024), nil
}
