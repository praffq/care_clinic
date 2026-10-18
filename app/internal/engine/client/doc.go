// Package client is the host-side Go client for care-engine-agent, the HTTP
// API served inside the engine VM (see package agent for the routes).
//
// The client knows nothing about vsock, Virtualization.framework or QEMU: the
// caller injects a DialFunc that returns one fresh connection to the agent,
// and every request is plain HTTP/1.1 over that connection with a fixed
// placeholder host. On macOS the VM driver dials the VZ virtio-socket device;
// on Windows TLSDialer dials the agent's mutual-TLS TCP port:
//
//	c := client.New(func(ctx context.Context) (net.Conn, error) { return vm.DialAgent(ctx) })
//	c := client.New(client.TLSDialer("127.0.0.1:47377", tlsConfig))
//
// Long-running ops stream protocol.Frames. Op and LoadImage call the
// FrameFunc for every progress and log frame, in order and on the caller's
// goroutine, and return when the terminal frame arrives: nil after decoding
// a result frame into the caller's value, or the *protocol.OpError carried
// by an error frame (codes such as agent.CodeNotInstalled, agent.CodeCanceled
// or agent.CodeCommandFailed). A stream that ends without a terminal frame
// returns ErrIncompleteStream; a frame longer than the line limit returns
// ErrLineTooLong.
//
// Errors are errors.Is/As friendly: a rejected request (HTTP 400) returns
// *protocol.ValidationError or *protocol.DecodeError, other non-success
// statuses return *APIError, which matches ErrBusy (409, with the running op
// in APIError.Op), ErrUnknownOp, ErrNotFound and ErrTooLarge. Params are also
// validated locally before anything is sent.
//
// Cancelling the context aborts the request, and the agent cancels read-only
// ops (stack.status, stack.logs) and image.load. Every other op is mutating:
// the agent detaches it from the request, so it keeps running in the VM and
// keeps holding the op lock after the client goes away. A cancelled or
// disconnected mutating op therefore has an unknown outcome; callers must
// re-query (Diagnostics().Busy, stack.status, ListBackups, ...) rather than
// assume it stopped or failed.
package client
