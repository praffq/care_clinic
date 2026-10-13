package vm

import (
	"fmt"
	"os"
)

// ManagedEngineAbsent proves that there is no installed or running managed
// engine and no data disk whose volumes need inspection before cleanup.
func ManagedEngineAbsent() (bool, error) {
	if !Supported() {
		return false, nil
	}
	return managedEngineAbsent([]string{currentFile(), dataDisk(), controlSocket()})
}

func managedEngineAbsent(paths []string) (bool, error) {
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			return false, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("could not inspect %s: %w", p, err)
		}
	}
	return true, nil
}
