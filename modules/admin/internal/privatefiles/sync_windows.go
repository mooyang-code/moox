package privatefiles

import "os"

// Windows does not expose directory fsync through os.File.Sync. Individual
// credential files are still flushed before their atomic rename.
func SyncDirectory(_ *os.Root) error { return nil }
