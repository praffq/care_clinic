package control

import "testing"

func TestPipeSDDL(t *testing.T) {
	got := pipeSDDL([]string{"S-1-5-21-1-2-3-1000"}, []string{"S-1-5-21-1-2-3-1009", "S-1-5-21-1-2-3-1001", "S-1-5-21-1-2-3-1001", "S-1-5-18"})
	want := "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;S-1-5-21-1-2-3-1000)(A;;0x120183;;;S-1-5-21-1-2-3-1009)(A;;0x120183;;;S-1-5-21-1-2-3-1001)"
	if got != want {
		t.Fatalf("pipeSDDL =\n%s\nwant\n%s", got, want)
	}
}
