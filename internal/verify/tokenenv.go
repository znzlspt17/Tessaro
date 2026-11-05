package verify

// ExportCompileEnv exposes only the existing isolated compiler environment.
func ExportCompileEnv(dir string) []string { return compileEnv(dir) }

// ExportExecuteEnv exposes only the existing isolated candidate environment.
func ExportExecuteEnv(dir string) []string { return executeEnv(dir) }
