package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func runNative(socket string) error {
	if !filepath.IsAbs(socket) {
		return errors.New("native agent socket must be absolute")
	}
	// The service manager creates this root-owned, group-accessible directory.
	info, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o007 != 0 {
		return errors.New("native agent socket directory must not be accessible to other users")
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("refusing to replace a non-socket agent endpoint")
		}
		c, err := net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			_ = c.Close()
			return errors.New("native agent is already running")
		}
		if err := os.Remove(socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	if err := os.Chmod(socket, 0o660); err != nil {
		return err
	}
	a := newAPI(dockerSocket)
	defer a.closeIdleConnections()
	a.native = true
	srv := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(listener); err != nil {
		return fmt.Errorf("native agent: %w", err)
	}
	return nil
}
