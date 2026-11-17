package phone

import "path/filepath"

func ServeLockPath(stateDir string) string { return filepath.Join(stateDir, "phone", "serve.lock") }
