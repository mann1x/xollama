package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/internal/fsowner"
	"github.com/ollama/ollama/types/xollama"
)

// settingsDefaults is the settings file's section holding the server's
// defaults for the models' settings.
const settingsDefaults = "defaults"

// settingsWrite serialises writers of the settings file: two tweak commands
// at once must not lose each other's change.
var settingsWrite sync.Mutex

// SettingsHandler reads and changes the server's own settings
// (plans/system-settings.md). Only from this machine, never through a
// proxy, as the API key is: the settings steer the host, and their values
// can include proxies and paths.
func (s *Server) SettingsHandler(c *gin.Context) {
	if !loopbackPeer(c) || proxied(c.Request) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "the server's settings are managed only from its own machine, not through a proxy"})
		return
	}
	var req api.SettingsRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	for name := range req.Envs {
		if err := envconfig.CheckOverride(name); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	if req.Defaults != nil {
		if err := req.Defaults.ValidateDefaults(); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	if req.GPU != nil {
		if err := req.GPU.Validate(); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	var restart []string
	if len(req.Envs) > 0 || req.Defaults != nil || req.GPU != nil {
		var err error
		if restart, err = applySettings(req); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	p, _ := envconfig.SettingsPath()
	c.JSON(http.StatusOK, api.SettingsResponse{Path: p, Envs: settingsEnvs(), Defaults: serverDefaults(), GPU: serverGPU(), Restart: restart})
}

// applySettings writes req's changes over the file as it is now, and
// returns the changed variables that wait for a restart.
func applySettings(req api.SettingsRequest) ([]string, error) {
	settingsWrite.Lock()
	defer settingsWrite.Unlock()

	cur, err := envconfig.ReadSettings()
	if err != nil {
		return nil, err
	}
	var restart []string
	for name, v := range req.Envs {
		old, had := cur.Envs[name]
		switch {
		case v == nil && !had:
			continue
		case v == nil:
			delete(cur.Envs, name)
		case had && old == *v:
			continue
		default:
			if cur.Envs == nil {
				cur.Envs = map[string]string{}
			}
			cur.Envs[name] = *v
		}
		if envconfig.NeedsRestart(name) {
			restart = append(restart, name)
		}
	}
	sort.Strings(restart)
	if req.Defaults != nil {
		if req.Defaults.IsZero() {
			delete(cur.Rest, settingsDefaults)
		} else {
			data, err := req.Defaults.Marshal()
			if err != nil {
				return nil, err
			}
			if cur.Rest == nil {
				cur.Rest = map[string]json.RawMessage{}
			}
			cur.Rest[settingsDefaults] = data
		}
	}
	if req.GPU != nil {
		if req.GPU.IsZero() {
			delete(cur.Rest, settingsGPU)
		} else {
			data, err := json.Marshal(req.GPU)
			if err != nil {
				return nil, err
			}
			if cur.Rest == nil {
				cur.Rest = map[string]json.RawMessage{}
			}
			cur.Rest[settingsGPU] = data
		}
	}
	if err := writeSettings(cur); err != nil {
		return nil, err
	}
	envconfig.ReloadSettings()
	return restart, nil
}

// writeSettings replaces the settings file atomically, mode 0600, owned as
// the store's files are (store-ownership). Empty settings remove the file,
// so a server whose overrides were all cleared is back to upstream's state.
func writeSettings(st envconfig.Settings) error {
	p, err := envconfig.SettingsPath()
	if err != nil {
		return err
	}
	if len(st.Envs) == 0 && len(st.Rest) == 0 {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := fsowner.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := fsowner.CreateTemp(filepath.Dir(p), ".xollama-settings-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// settingsEnvs lists every variable the server reads, under the spelling
// its own documentation uses, plus any other the tweak file sets: those are
// read by the engines, not by this server.
func settingsEnvs() []api.SettingsEnv {
	known := envconfig.AsMap()
	names := make(map[string]bool, len(known))
	for name := range known {
		names[name] = true
	}
	st, _ := envconfig.ReadSettings()
	for name := range st.Envs {
		names[name] = true
	}

	out := make([]api.SettingsEnv, 0, len(names))
	for name := range names {
		e := api.SettingsEnv{Name: name, Restart: envconfig.NeedsRestart(name)}
		if v, ok := known[name]; ok {
			e.Description = v.Description
		} else {
			e.External = !strings.HasPrefix(name, envconfig.Prefix) && !strings.HasPrefix(name, "OLLAMA_")
		}
		src := envconfig.LookupSource(name)
		e.Value = src.Value
		if src.HasTweak {
			e.Tweak, e.Source = &src.Tweak, "tweak"
		}
		if src.HasEnv {
			e.Env = &src.Env
			if e.Source == "" {
				e.Source = "env"
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// serverDefaults are the server's defaults for the models' settings, nil
// when it has none. Defaults that no longer validate -- a file edited by
// hand, or written by a newer build -- apply nothing, and say so.
func serverDefaults() *xollama.Config {
	raw := envconfig.SettingsSection(settingsDefaults)
	if len(raw) == 0 {
		return nil
	}
	d, err := xollama.Parse(raw)
	if err == nil {
		err = d.ValidateDefaults()
	}
	if err != nil {
		slog.Warn("the server's default settings are ignored", "error", err)
		return nil
	}
	return d
}

// launchXollama is the config a model runs with: its own settings, and the
// server's defaults for those it does not state. A default this model cannot
// act on is left out and logged, never a reason to refuse the model.
func launchXollama(m *Model) *xollama.Config {
	own := m.Xollama
	cfg, skipped := own.WithDefaults(serverDefaults())
	for _, s := range skipped {
		slog.Info("server default not applied to this model", "model", m.ShortName, "default", s)
	}
	return cfg
}
