package clinic

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/ohcnetwork/care_desktop/app/internal/backup"
)

func (e *Clinic) stopWorkers() error {
	if err := e.dc("stop", "celery-worker", "celery-beat"); err != nil {
		return fmt.Errorf("could not stop workers and the scheduler; no migrations were attempted: %w", err)
	}
	ids, err := e.captureLines("docker", "compose", "ps", "--all", "--quiet", "celery-worker", "celery-beat")
	if err != nil {
		return fmt.Errorf("could not verify that workers stopped; no migrations were attempted: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	states, err := e.captureLines("docker", append([]string{"inspect", "--format", "{{.State.Status}}"}, ids...)...)
	if err != nil {
		return err
	}
	if len(states) != len(ids) {
		return fmt.Errorf("could not verify every worker's state; no migrations were attempted")
	}
	for _, state := range states {
		if state != "exited" && state != "created" {
			return fmt.Errorf("a worker or scheduler is still %s; no migrations were attempted", state)
		}
	}
	return nil
}

func (e *Clinic) migrateStaged(database, restoreID string) error {
	if len(restoreID) != 24 || database != "care_restore_"+restoreID {
		return fmt.Errorf("invalid staged restore database")
	}
	if _, err := hex.DecodeString(restoreID); err != nil {
		return fmt.Errorf("invalid restore identity: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(e.InstallDir, "backend.env"))
	if err != nil {
		return err
	}
	env, err := dotenv.Parse(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("cannot read staged migration settings: %w", err)
	}
	databaseURL, err := stagedDatabaseURL(env, database)
	if err != nil {
		return err
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return fmt.Errorf("invalid staged database URL")
	}
	query := u.Query()
	query.Set("application_name", composeProject+":restore:"+restoreID)
	u.RawQuery = query.Encode()
	databaseURL = u.String()
	return e.run([]string{"POSTGRES_DB=" + database, "DATABASE_URL=" + databaseURL, "PGAPPNAME=" + composeProject + ":restore:" + restoreID},
		"docker", "compose", "run", "--rm", "--no-deps",
		"--name", composeProject+"-restore-"+restoreID+"-migrate",
		"--label", backup.RestoreLabel+"="+restoreID,
		"--entrypoint", "python", "-e", "POSTGRES_DB", "-e", "DATABASE_URL", "-e", "PGAPPNAME",
		"backend", "manage.py", "migrate", "--noinput")
}

func stagedDatabaseURL(env map[string]string, database string) (string, error) {
	var u *url.URL
	if raw := env["DATABASE_URL"]; raw != "" {
		var err error
		u, err = url.Parse(raw)
		if err != nil || u.Hostname() == "" || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			return "", fmt.Errorf("DATABASE_URL is not a valid PostgreSQL connection URL")
		}
	} else {
		value := func(key, fallback string) string {
			if v := env[key]; v != "" {
				return v
			}
			return fallback
		}
		u = &url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(value("POSTGRES_USER", "postgres"), env["POSTGRES_PASSWORD"]),
			Host:   net.JoinHostPort(value("POSTGRES_HOST", "db"), value("POSTGRES_PORT", "5432")),
		}
	}
	u.Path, u.RawPath = "/"+database, ""
	query := u.Query()
	query.Del("dbname")
	query.Del("database")
	u.RawQuery = query.Encode()
	return u.String(), nil
}
