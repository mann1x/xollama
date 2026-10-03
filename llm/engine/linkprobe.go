package engine

// LinkProbeCommand is the command line that has the engine measure the host
// link of every GPU of backend b and print one "opencoti.link-probe/1" JSON
// document on stdout. It loads no model and starts no server (opencoti
// patch 0499; builds from b62 on).
func LinkProbeCommand(artifact string, b Backend, goos string) (string, []string) {
	args := []string{"--server", "--link-probe", "--gpu", gpuFlag([]Device{{Backend: b}})}
	return run(artifact, args, goos)
}
