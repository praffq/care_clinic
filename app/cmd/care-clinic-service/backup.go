//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/backups"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
)

const backupUsage = `usage: care-clinic-service ctl [--dev] backup <command>

commands:
  run                take a backup in the engine now and copy it to backup_dir
  list               sets in backup_dir and still in the engine
  status             export state (last copied, errors, pending sets)
  restore [--recovery-key file] <id>
                     replace the clinic's database and files with the set
                     <id> from backup_dir (default key: the one saved by
                     ctl clinic install)
`

var errBackupUsage = errors.New("backup usage")

func backupCtl(ctx context.Context, c *control.Client, args []string) (any, error) {
	if len(args) == 0 {
		return nil, errBackupUsage
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "run":
		if len(args) != 0 {
			return nil, errBackupUsage
		}
		return backupRun(ctx, c)
	case "list":
		if len(args) != 0 {
			return nil, errBackupUsage
		}
		return c.Backups(ctx)
	case "status":
		if len(args) != 0 {
			return nil, errBackupUsage
		}
		return c.BackupStatus(ctx)
	case "restore":
		return nil, backupRestore(ctx, c, args)
	}
	return nil, errBackupUsage
}

func printFrame(f protocol.Frame) {
	b, _ := json.Marshal(f)
	_, _ = fmt.Fprintf(os.Stderr, "%s\n", b)
}

func backupRun(ctx context.Context, c *control.Client) (any, error) {
	var res protocol.BackupRunResult
	if err := c.Agent().Op(ctx, protocol.BackupRun{}, &res, printFrame); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "backup", res.BackupID, "taken; copying it out")
	st, err := c.ExportBackups(ctx)
	if err != nil {
		return nil, err
	}
	if st.LastError != "" {
		_ = printJSON(st)
		return nil, fmt.Errorf("export: %s", st.LastError)
	}
	return st, nil
}

func backupRestore(ctx context.Context, c *control.Client, args []string) error {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	keyPath := fs.String("recovery-key", "", "recovery key PEM (default "+stack.BackupRecoveryKey+" in the clinic state dir)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return errBackupUsage
	}
	id := fs.Arg(0)
	if *keyPath == "" {
		state, err := stack.DefaultStateDir()
		if err != nil {
			return err
		}
		*keyPath = filepath.Join(state, stack.BackupRecoveryKey)
	}
	key, err := stack.ReadRecoveryKey(*keyPath)
	if err != nil {
		return err
	}
	cfg, err := c.Config(ctx)
	if err != nil {
		return err
	}
	if err := (protocol.DBRestore{BackupID: id}).Validate(); err != nil {
		return err
	}
	dir := filepath.Join(cfg.BackupDir, id)
	fmt.Fprintln(os.Stderr, "restoring", dir, "- the clinic is down until it finishes")
	if err := backups.Restore(ctx, c.Agent(), dir, key, printFrame); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "restored", id)
	return nil
}
