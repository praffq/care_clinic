package control

import "strings"

// pipeClientAccess is what control clients get on the pipe: read and write
// data and attributes, READ_CONTROL and SYNCHRONIZE. It deliberately leaves
// out FILE_APPEND_DATA, which on a pipe is FILE_CREATE_PIPE_INSTANCE (part
// of GENERIC_WRITE): a client must not be able to serve the pipe itself.
const pipeClientAccess = "0x120183"

// pipeSDDL returns the control pipe's security descriptor: a protected DACL
// with full access for SYSTEM and Administrators (owners lists more SIDs to
// treat that way) and client access for clients (SID strings).
func pipeSDDL(owners, clients []string) string {
	var b strings.Builder
	b.WriteString("D:P(A;;GA;;;SY)(A;;GA;;;BA)")
	seen := map[string]bool{"SY": true, "BA": true, "S-1-5-18": true, "S-1-5-32-544": true}
	for _, sid := range owners {
		if !seen[sid] {
			seen[sid] = true
			b.WriteString("(A;;GA;;;" + sid + ")")
		}
	}
	for _, sid := range clients {
		if !seen[sid] {
			seen[sid] = true
			b.WriteString("(A;;" + pipeClientAccess + ";;;" + sid + ")")
		}
	}
	return b.String()
}
