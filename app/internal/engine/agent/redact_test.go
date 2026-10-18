package agent

import (
	"fmt"
	"slices"
	"testing"
)

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"POSTGRES_PASSWORD=hunter2", "POSTGRES_PASSWORD=***"},
		{"password: hunter2 next", "password: *** next"},
		{`{"secret_key": "abc def"}`, `{"secret_key": ***}`},
		{"api_key=abc&x=1", "api_key=***&x=1"},
		{"MINIO_SECRET_KEY='q w'", "MINIO_SECRET_KEY=***"},
		{"Token=xyz;", "Token=***;"},
		{"Authorization: Bearer eyJhbGciOi.payload.sig", "Authorization: Bearer ***"},
		{"DATABASE_URL=postgres://care:pw@db:5432/care", "DATABASE_URL=postgres://care:***@db:5432/care"},
		{"connecting to postgresql://u:pw@host/db now", "connecting to postgresql://u:***@host/db now"},
		{"redis://:pw@redis:6379/0", "redis://:***@redis:6379/0"},
		{"postgres://care@db/care", "postgres://care@db/care"},
		{"Container care-db-1 Started", "Container care-db-1 Started"},
		{"Loaded image: care/backend:1.2", "Loaded image: care/backend:1.2"},
	} {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLogRing(t *testing.T) {
	r := NewLogRing(3)
	if got := r.Lines(); len(got) != 0 {
		t.Fatalf("empty ring = %v", got)
	}
	for i := range 5 {
		_, _ = fmt.Fprintf(r, "line %d token=t%d\n", i, i)
	}
	_, _ = fmt.Fprint(r, "partial")
	want := []string{"line 2 token=***", "line 3 token=***", "line 4 token=***"}
	if got := r.Lines(); !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}
