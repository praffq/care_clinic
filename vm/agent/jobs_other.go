//go:build !linux

package main

import "os/exec"

func configureJob(cmd *exec.Cmd) {}
