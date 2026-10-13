package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fakeEngine(calls *[][]string, fail map[int]error) engineSync {
	return engineSync{
		sensitive: func(args []string) bool { return len(args) > 0 && args[0] == "run" },
		rewrite: func(args []string) ([]string, []string) {
			return append([]string{"rewritten"}, args...), []string{"/data"}
		},
		sync: func(ctx context.Context, paths []string) error {
			if _, ok := ctx.Deadline(); !ok {
				return errors.New("sync has no deadline")
			}
			*calls = append(*calls, paths)
			return fail[len(*calls)]
		},
		timeout: time.Minute,
	}
}

func TestEngineHookSyncsBeforeAndAfter(t *testing.T) {
	var calls [][]string
	out, after, err := engineDockerHook(fakeEngine(&calls, nil))([]string{"run", "x"})
	if err != nil || len(out) != 3 || out[0] != "rewritten" {
		t.Fatalf("out %v err %v", out, err)
	}
	if err := after(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || len(calls[0]) != 1 || calls[1] != nil {
		t.Fatalf("calls = %v", calls)
	}
}

func TestEngineHookPrepareFailureIsReturned(t *testing.T) {
	boom := errors.New("disk full")
	var calls [][]string
	out, after, err := engineDockerHook(fakeEngine(&calls, map[int]error{1: boom}))([]string{"run"})
	if !errors.Is(err, boom) || out != nil || after != nil {
		t.Fatalf("out %v after %v err %v", out, after != nil, err)
	}
}

func TestEngineHookFinishFailureIsReturned(t *testing.T) {
	boom := errors.New("copy back failed")
	var calls [][]string
	_, after, err := engineDockerHook(fakeEngine(&calls, map[int]error{2: boom}))([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := after(); !errors.Is(err, boom) {
		t.Fatalf("after err = %v", err)
	}
}

func TestEngineHookSkipsInsensitiveCommands(t *testing.T) {
	var calls [][]string
	out, after, err := engineDockerHook(fakeEngine(&calls, nil))([]string{"ps"})
	if err != nil || after() != nil || len(out) != 1 || out[0] != "ps" || len(calls) != 0 {
		t.Fatalf("out %v err %v calls %v", out, err, calls)
	}
}
