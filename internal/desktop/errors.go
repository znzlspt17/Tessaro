package desktop

type Stage string

const (
	StageWindowCreate Stage = "window_create"
	StageShutdown     Stage = "shutdown"
)

type StageError struct{ Stage Stage }

func (e *StageError) Error() string { return string(e.Stage) }
