package protocol

// HTTP-level names shared by the agent and its client.
const (
	// ParamsQuery carries image.load params, URL-encoded JSON, since the
	// request body is the image tarball.
	ParamsQuery = "params"
	// BackupSHA256Header carries the manifest SHA-256 (hex) of an exported
	// backup file.
	BackupSHA256Header = "X-Care-SHA256"
)
