package engine

// StockShipped reports whether xOllama ships stock llama-server on goos.
//
// macOS does not (owner, 2026-10-05, carried out with the c10 release once
// opencoti served the Clef head): every GGUF load on Apple silicon runs on
// opencoti-llamafile through Metal, and there is no stock engine to fall back
// to. A load that would have gone to stock -- XOLLAMA_ENGINE=llamacpp, a model
// pinned to engine=llamacpp, a missing artifact or loader -- is refused with
// the reason instead. Windows and Linux keep stock llama-server.
func StockShipped(goos string) bool {
	return goos != "darwin"
}

// StocklessRefusal is the error text for a load that did not get opencoti on a
// platform without stock llama-server.
func StocklessRefusal(goos string) string {
	return "this build runs every model on the opencoti engine and ships no stock llama-server on " + goos +
		"; the load did not get opencoti (check " + EnvSelector + " is unset or auto, that the model does not pin engine=llamacpp, and that the engine and its loader are installed)"
}
