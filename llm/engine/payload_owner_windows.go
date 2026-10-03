package engine

// AdoptPayloadHome has nothing to do on Windows: the engine runs as the user
// who owns the install. See payload_owner_unix.go.
func AdoptPayloadHome(string) {}

var payloadForeign = func(string) error { return nil }
