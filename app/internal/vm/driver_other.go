//go:build !darwin && !windows

package vm

import "context"

func boot(context.Context, bootOptions) (machine, error) { return nil, errUnsupported }
