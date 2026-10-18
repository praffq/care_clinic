package atomicfile

// SyncDir does nothing on Windows: a directory handle can't be flushed
// (FlushFileBuffers needs write access and fails with "Access is denied"),
// and NTFS journals directory changes itself.
func SyncDir(string) error { return nil }
