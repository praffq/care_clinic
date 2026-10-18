package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/clinic"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// webPasswordApp answers manage ops like a 0.1.3 engine; it returns the
// manage calls made.
func webPasswordApp(t *testing.T, lines ...string) (*App, func() []agent.FakeCall) {
	t.Helper()
	a := settingsApp(t)
	svc := a.service.(*fakeAPI).Service
	status := svc.Fake.Handle
	if len(lines) == 0 {
		lines = []string{protocol.ManagePasswordSet}
	}
	svc.Fake.Handle = func(ctx context.Context, op protocol.Op, em agent.Emitter) (any, error) {
		if _, ok := op.(*protocol.ManageCmd); !ok {
			return status(ctx, op, em)
		}
		for _, l := range lines {
			em.Log(protocol.LogLine{Service: protocol.ServiceBackend, Stream: "stdout", Line: l})
		}
		if lines[0] != protocol.ManagePasswordSet {
			return nil, &protocol.OpError{Code: protocol.CodeCommandFailed, Message: "exit status 3"}
		}
		return nil, nil
	}
	return a, func() []agent.FakeCall {
		var out []agent.FakeCall
		for _, c := range svc.Fake.Calls() {
			if c.Method == "Manage" {
				out = append(out, c)
			}
		}
		return out
	}
}

func TestResetWebPasswordUsesTheUnlockedAdminPassword(t *testing.T) {
	a, calls := webPasswordApp(t)
	if err := a.ResetWebPassword(" admin ", "NewWebSecret-918", settingsPassword); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 1 || string(got[0].Password) != "NewWebSecret-918" {
		t.Fatalf("the backend did not receive the new password: %+v", got)
	}
	if op := got[0].Op.(*protocol.ManageCmd); op.Command != protocol.ManageChangePassword || op.Username != "admin" {
		t.Fatalf("op = %+v", op)
	}
	if !a.VerifyAdminPassword(settingsPassword) {
		t.Fatal("resetting a web password changed the CARE Clinic admin password")
	}
}

func TestResetWebPasswordReportsCareAnswers(t *testing.T) {
	a, _ := webPasswordApp(t, protocol.ManagePasswordNoUser)
	if err := a.ResetWebPassword("Nurse", "NewWebSecret-918", settingsPassword); err == nil ||
		!strings.Contains(err.Error(), `no CARE user named "Nurse"`) {
		t.Fatalf("missing user: %v", err)
	}
	a, _ = webPasswordApp(t, protocol.ManagePasswordInvalid+"This password is too common.")
	if err := a.ResetWebPassword("admin", "password1", settingsPassword); err == nil ||
		err.Error() != "CARE didn't accept that password: This password is too common." {
		t.Fatalf("rejected password: %v", err)
	}
}

func TestResetWebPasswordRejectsAWrongAdminPassword(t *testing.T) {
	a, calls := webPasswordApp(t)
	err := a.ResetWebPassword("admin", "NewWebSecret-918", "WrongPassword123")
	if err == nil || !strings.Contains(err.Error(), "admin password does not match") {
		t.Fatalf("got %v", err)
	}
	if len(calls()) != 0 {
		t.Fatal("the password change ran before the admin password was checked")
	}
}

func TestResetWebPasswordRefusesWhenCareCannotTakeIt(t *testing.T) {
	for _, state := range []string{"stopped", "not installed", "busy", "empty username"} {
		t.Run(state, func(t *testing.T) {
			a, calls := webPasswordApp(t)
			user, want := "admin", ""
			switch state {
			case "stopped":
				t.Setenv("CARE_FAKE_STATE", "stopped")
				want = clinic.ErrCareNotRunning.Error()
			case "not installed":
				a.cfg.SetupDone = false
				want = "admin password does not match"
			case "busy":
				a.jobMu.Lock()
				defer a.jobMu.Unlock()
				want = "something else is still running"
			case "empty username":
				user, want = "  ", "enter the CARE username"
			}
			err := a.ResetWebPassword(user, "NewWebSecret-918", settingsPassword)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want %q", err, want)
			}
			if len(calls()) != 0 {
				t.Fatal("the password change ran anyway")
			}
		})
	}
}
