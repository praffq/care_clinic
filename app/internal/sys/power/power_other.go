//go:build !darwin && !linux && !windows

package power

func OnACPower() (bool, bool, error) { return false, false, nil }
