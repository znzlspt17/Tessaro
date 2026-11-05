package tokenverify

import (
	"errors"
	"time"
)

type Binding struct {
	Name       string   `json:"name"`
	Params     []string `json:"params"`
	ReturnType string   `json:"return_type"`
	Locals     int      `json:"locals"`
	Source     string   `json:"source"`
}
type Timing struct {
	Compile, Execute time.Duration
	Compiled         bool
}

var ErrCompileTimeout = errors.New("compile timeout")
var ErrExecutionTimeout = errors.New("execution timeout")
var ErrCompileFailed = errors.New("candidate compile failed")
