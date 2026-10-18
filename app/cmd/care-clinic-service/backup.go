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
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/backups"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
)

const backupUsage = `usage: care-clinic-service ctl [--dev] backup <command>

commands:
  run                take a backup in the engine now and copy it to backup_dir
  list               sets in backup_dir and still in the engine
  status             export state (last copied, errors, pending sets, extra copy)
  copy set <abs dir> keep an extra copy of the newest backup in <dir> (a USB
                     drive or network share); the folder must exist
  copy clear         stop keeping an extra copy (the copy is left in place)
  copy now           copy the newest backup to the extra copy folder now
  restore [--recovery-key file] [--from folder] [<id>]
                     replace the clinic's database and files with a backup
                     set: <id> in backup_dir, or from --from, which is a set
                     folder or a folder of sets (the extra copy, or a backup
                     folder brought from another computer; its newest set
                     when <id> is left out). The default key is the one
                     saved by ctl clinic install; a backup made on another
                     computer needs that computer's recovery key.
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
	case "copy":
		return backupCopy(ctx, c, args)
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

func backupCopy(ctx context.Context, c *control.Client, args []string) (any, error) {
	var dir string
	switch {
	case len(args) == 2 && args[0] == "set":
		dir = args[1]
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%s is not a folder; connect the drive or create the folder first", dir)
		}
	case len(args) == 1 && args[0] == "clear":
	case len(args) == 1 && args[0] == "now":
		st, err := c.CopyBackups(ctx)
		if err != nil {
			return nil, err
		}
		if st.Copy == nil {
			return nil, errors.New("no extra copy folder is set (ctl backup copy set <dir>)")
		}
		if st.Copy.LastError != "" {
			_ = printJSON(st)
			return nil, fmt.Errorf("copy: %s", st.Copy.LastError)
		}
		return st, nil
	default:
		return nil, errBackupUsage
	}
	if _, err := c.UpdateConfig(ctx, api.ServiceConfigUpdate{BackupCopyDir: &dir}); err != nil {
		return nil, err
	}
	if dir == "" {
		return c.BackupStatus(ctx)
	}
	return c.CopyBackups(ctx)
}

func backupRestore(ctx context.Context, c *control.Client, args []string) error {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	keyPath := fs.String("recovery-key", "", "recovery key PEM (default "+stack.BackupRecoveryKey+" in the clinic state dir)")
	from := fs.String("from", "", "a set folder or a folder of sets (default backup_dir)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 1 || (fs.NArg() == 0 && *from == "") {
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
	if *from == "" {
		cfg, err := c.Config(ctx)
		if err != nil {
			return err
		}
		*from = cfg.BackupDir
	}
	dir, err := backups.FindSet(*from, id)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "restoring", dir, "- the clinic is down until it finishes")
	if err := backups.Restore(ctx, c.Agent(), dir, key, printFrame); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "restored", filepath.Base(dir))
	return nil
}
