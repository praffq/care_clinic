package applog

import (
	"os"
	"runtime"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/logdir"
)

const logStem = "care-log"

const logName = logStem + ".log"

func DefaultLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return logdir.Default(runtime.GOOS, home, os.Getenv)
}
