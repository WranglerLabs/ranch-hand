//go:build windows

package lifecycle

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

func replaceFile(source, destination string) error {
	sourcePath, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPath, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	var replaceErr error
	for attempt := 0; attempt < 6; attempt++ {
		replaceErr = windows.MoveFileEx(sourcePath, destinationPath, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if replaceErr == nil {
			return nil
		}
		if !errors.Is(replaceErr, windows.ERROR_ACCESS_DENIED) && !errors.Is(replaceErr, windows.ERROR_SHARING_VIOLATION) {
			return replaceErr
		}
		if attempt < 5 {
			time.Sleep(time.Duration(1<<attempt) * 10 * time.Millisecond)
		}
	}
	return replaceErr
}
