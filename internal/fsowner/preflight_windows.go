package fsowner

// Preflight has nothing to check on Windows: Ollama installs per user, so there
// is no service account whose files an ordinary user could make unusable.
func Preflight(...string) {}
