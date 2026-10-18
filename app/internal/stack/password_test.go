package stack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/stack/stacktest"
)

func TestChangePassword(t *testing.T) {
	const pw = "N3w-Secret-Pass"
	for _, tc := range []struct {
		name  string
		lines []string
		err   error
		check func(t *testing.T, err error)
	}{
		{name: "set", lines: []string{protocol.ManagePasswordSet}, check: func(t *testing.T, err error) {
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no user", lines: []string{protocol.ManagePasswordNoUser},
			err: &protocol.OpError{Code: protocol.CodeCommandFailed, Message: "exit status 3"},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrNoUser) {
					t.Fatalf("err = %v", err)
				}
			}},
		{name: "old engine missing user",
			err: &protocol.OpError{Code: protocol.CodeCommandFailed, Message: "manage changepassword failed: users.models.User.DoesNotExist: User matching query does not exist."},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrNoUser) {
					t.Fatalf("err = %v", err)
				}
			}},
		{name: "rejected", lines: []string{
			protocol.ManagePasswordInvalid + "This password is too common.",
			protocol.ManagePasswordInvalid + "Too similar to " + pw + ".",
		}, err: &protocol.OpError{Code: protocol.CodeCommandFailed, Message: "exit status 4"},
			check: func(t *testing.T, err error) {
				var rej *PasswordRejectedError
				if !errors.As(err, &rej) || len(rej.Problems) != 2 {
					t.Fatalf("err = %v", err)
				}
				if strings.Contains(err.Error(), pw) || !strings.Contains(err.Error(), "too common") {
					t.Fatalf("message = %q", err)
				}
			}},
		{name: "other failure", err: &protocol.OpError{Code: protocol.CodeCommandFailed, Message: "backend is not running"},
			check: func(t *testing.T, err error) {
				if err == nil || errors.Is(err, ErrNoUser) || !strings.Contains(err.Error(), "not running") {
					t.Fatalf("err = %v", err)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := stacktest.New(t)
			svc.Fake.Handle = func(_ context.Context, _ protocol.Op, em agent.Emitter) (any, error) {
				for _, l := range tc.lines {
					em.Log(protocol.LogLine{Service: protocol.ServiceBackend, Stream: "stdout", Line: l})
				}
				return nil, tc.err
			}
			r := &Runtime{Service: svc, StateDir: t.TempDir()}
			tc.check(t, r.ChangePassword(ctxT(t), "nurse", pw))
			calls := svc.Fake.Calls()
			if len(calls) != 1 || string(calls[0].Password) != pw {
				t.Fatalf("calls = %+v", calls)
			}
			op := calls[0].Op.(*protocol.ManageCmd)
			if op.Command != protocol.ManageChangePassword || op.Username != "nurse" || strings.Contains(op.PasswordFile, pw) {
				t.Errorf("op = %+v", op)
			}
			if left, _ := os.ReadDir(filepath.Join(svc.Files, "config")); len(left) != 0 {
				t.Errorf("password file left behind: %v", left)
			}
		})
	}
}
