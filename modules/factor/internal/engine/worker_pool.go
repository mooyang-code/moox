package engine

// WorkerPoolStatus is a lightweight runtime status snapshot.
type WorkerPoolStatus struct {
	Workers        int
	Next           uint64
	Ready          bool
	WorkerVersion  string
	PythonVersion  string
	RuntimeEnvHash string
	ArrowAvailable bool
}
