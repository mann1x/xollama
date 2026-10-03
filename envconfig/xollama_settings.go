package envconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

// The server's own settings, set with `xollama tweak` instead of the
// environment (plans/system-settings.md).
//
// The environment is how upstream configures a server, and it is awkward to
// change: a global variable or a systemd override, the documentation to find
// the name, and a restart. SettingsFile holds the same settings as real
// configuration, written by the server itself through /api/xollama/settings.
//
// Its envs section overrides the environment: the owner's ruling is that the
// tweak file wins, so a value set here beats the same variable in the
// process environment, under either spelling. With no file, or an empty
// envs section, nothing changes -- off means off.

// SettingsFile is {"envs": {...}, ...} in the server's ~/.ollama, mode 0600.
// Sections other than envs belong to the packages that read them; this file
// keeps them as they were written.
const SettingsFile = "xollama-settings.json"

// SettingsPath is where the server keeps its settings.
func SettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ollama", SettingsFile), nil
}

// Settings is the file's content.
type Settings struct {
	// Envs maps a variable's name to the value that overrides it.
	Envs map[string]string `json:"envs,omitempty"`
	// Rest keeps every other section verbatim.
	Rest map[string]json.RawMessage `json:"-"`
}

// MarshalJSON writes envs beside the other sections.
func (s Settings) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(s.Rest)+1)
	for k, v := range s.Rest {
		out[k] = v
	}
	if len(s.Envs) > 0 {
		out["envs"] = s.Envs
	}
	return json.MarshalIndent(out, "", "  ")
}

// UnmarshalJSON reads envs and keeps every other section.
func (s *Settings) UnmarshalJSON(data []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	*s = Settings{}
	if raw, ok := all["envs"]; ok {
		if err := json.Unmarshal(raw, &s.Envs); err != nil {
			return fmt.Errorf("envs: %w", err)
		}
		delete(all, "envs")
	}
	if len(all) > 0 {
		s.Rest = all
	}
	return nil
}

var (
	settingsMu     sync.RWMutex
	settingsLoaded bool
	settings       Settings
)

// ReadSettings reads the file. A missing file is empty settings, not an
// error.
func ReadSettings() (Settings, error) {
	p, err := SettingsPath()
	if err != nil {
		return Settings{}, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("%s: %w", p, err)
	}
	return s, nil
}

// envOverrides is the envs section, read once and again on ReloadSettings.
// A file that cannot be read or parsed overrides nothing: the server must
// still start on its environment, and says why (cachedSettings).
func envOverrides() map[string]string {
	return cachedSettings().Envs
}

// SettingsSection is a section of the settings file other than envs, as
// last read, or nil when the file has none.
func SettingsSection(name string) json.RawMessage {
	return cachedSettings().Rest[name]
}

// cachedSettings is the file as last read; it is read again after
// ReloadSettings.
func cachedSettings() Settings {
	settingsMu.RLock()
	if settingsLoaded {
		defer settingsMu.RUnlock()
		return settings
	}
	settingsMu.RUnlock()

	s, err := ReadSettings()
	if err != nil {
		slog.Warn("xollama settings ignored; the environment applies", "error", err)
	}

	settingsMu.Lock()
	defer settingsMu.Unlock()
	if !settingsLoaded {
		settings, settingsLoaded = s, true
	}
	return settings
}

// ReloadSettings makes the next read see the file as it is now. The server
// calls it after every write.
func ReloadSettings() {
	settingsMu.Lock()
	settingsLoaded = false
	settings = Settings{}
	settingsMu.Unlock()
}

// override returns the tweak file's value for key, under its XOLLAMA_
// spelling first. ok is false when the file names neither spelling.
func override(key string) (string, bool) {
	envs := envOverrides()
	if len(envs) == 0 {
		return "", false
	}
	if x := XollamaKey(key); x != "" {
		if v, ok := envs[x]; ok {
			return trimVar(v), true
		}
	}
	v, ok := envs[key]
	return trimVar(v), ok
}

// Environ is os.Environ with the tweak file's overrides applied, for a
// process the server starts: an engine reads its own LLAMA_ARG_*,
// OPENCOTI_* and *_VISIBLE_DEVICES, and must see what the server sees.
func Environ() []string {
	env := os.Environ()
	envs := envOverrides()
	if len(envs) == 0 {
		return env
	}
	applied := make(map[string]bool, len(envs))
	for i, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for name, v := range envs {
			if strings.EqualFold(k, name) {
				env[i] = name + "=" + v
				applied[name] = true
			}
		}
	}
	names := make([]string, 0, len(envs))
	for name := range envs {
		if !applied[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		env = append(env, name+"="+envs[name])
	}
	return env
}

// passThroughPrefixes are variables the engines and GPU runtimes read
// themselves. The tweak file accepts them although no getter here names
// them: Environ hands them to the engine.
var passThroughPrefixes = []string{"LLAMA_ARG_", "OPENCOTI_", "GGML_", "CUDA_", "HIP_", "ROCR_", "HSA_"}

// refusedOverrides cannot be set in the tweak file. The API key has its own
// command and keeps only its digest on disk; a key in plain text in a
// settings file would undo that.
var refusedOverrides = []string{"XOLLAMA_API_KEY", "OLLAMA_API_KEY"}

// restartOverrides are read once, when the server starts, so a change waits
// for a restart. Every other variable is read again where it is used.
var restartOverrides = []string{
	"OLLAMA_HOST", "XOLLAMA_HOST", "OLLAMA_MODELS", "OLLAMA_ORIGINS", "OLLAMA_DEBUG",
	"OLLAMA_NOPRUNE", "OLLAMA_LLM_LIBRARY", "OLLAMA_VULKAN", "OLLAMA_IGPU_ENABLE",
	"CUDA_VISIBLE_DEVICES", "HIP_VISIBLE_DEVICES", "ROCR_VISIBLE_DEVICES",
	"GGML_VK_VISIBLE_DEVICES", "GPU_DEVICE_ORDINAL", "HSA_OVERRIDE_GFX_VERSION",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
}

// CheckOverride says whether name may be set in the tweak file, and why not.
func CheckOverride(name string) error {
	if name == "" || strings.ContainsAny(name, "= \t\n") {
		return fmt.Errorf("%q is not a variable name", name)
	}
	if slices.Contains(refusedOverrides, name) {
		return fmt.Errorf("%s is set with `xollama tweak server --api-key`, which keeps only the key's digest on disk", name)
	}
	if _, ok := AsMap()[name]; ok {
		return nil
	}
	if x := XollamaKey(name); x != "" {
		if _, ok := AsMap()[x]; ok {
			return nil
		}
	}
	if rest, ok := strings.CutPrefix(name, Prefix); ok {
		if _, ok := AsMap()["OLLAMA_"+rest]; ok {
			return nil
		}
	}
	for _, p := range passThroughPrefixes {
		if strings.HasPrefix(name, p) {
			return nil
		}
	}
	return fmt.Errorf("%s is not a setting xollama or its engines read; `xollama tweak show envs` lists them", name)
}

// NeedsRestart says whether a change to name waits for the server to start
// again.
func NeedsRestart(name string) bool {
	if slices.Contains(restartOverrides, name) {
		return true
	}
	if rest, ok := strings.CutPrefix(name, Prefix); ok {
		return slices.Contains(restartOverrides, "OLLAMA_"+rest)
	}
	return false
}

// EnvSource is where a variable's effective value comes from.
type EnvSource struct {
	Name string
	// Value is the effective value: the tweak file's when it sets one, else
	// the environment's.
	Value string
	// Tweak and Env are the two candidates. A value the tweak file
	// overrides is still reported, so nothing is hidden.
	Tweak, Env       string
	HasTweak, HasEnv bool
}

// LookupSource reports a variable's tweak-file and environment values. It
// reads the name as given, not its other spelling.
//
// It reads both spellings as Var does: for an OLLAMA_ name, the XOLLAMA_
// spelling first, in the tweak file and in the environment alike.
func LookupSource(name string) EnvSource {
	s := EnvSource{Name: name}
	s.Tweak, s.HasTweak = override(name)
	if x := XollamaKey(name); x != "" {
		if v, ok := os.LookupEnv(x); ok && trimVar(v) != "" {
			s.Env, s.HasEnv = trimVar(v), true
		}
	}
	if !s.HasEnv {
		if v, ok := os.LookupEnv(name); ok {
			s.Env, s.HasEnv = trimVar(v), true
		}
	}
	if s.HasTweak {
		s.Value = s.Tweak
	} else {
		s.Value = s.Env
	}
	return s
}

// overrideOnly is the tweak file's value for exactly name.
func overrideOnly(name string) (string, bool) {
	v, ok := envOverrides()[name]
	return trimVar(v), ok
}

// LookupEnv is os.LookupEnv with the tweak file's value first.
func LookupEnv(name string) (string, bool) {
	if v, ok := overrideOnly(name); ok {
		return v, true
	}
	return os.LookupEnv(name)
}
