package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

// MediaVoicesHandler serves /api/xollama/media/voices: a speech model's
// voices for a picker. The model's own come from its engine (/props
// media.tts), so the engine is started when it is not running; the
// template's voice_map names them for OpenAI clients.
func (s *Server) MediaVoicesHandler(c *gin.Context) {
	name := c.Query("model")
	if name == "" && c.Request.Method == http.MethodPost {
		var req api.MediaVoicesRequest
		if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil && err != io.EOF {
			mediaError(c, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		name = req.Model
	}
	name = mediaName(name, CapabilitySpeech)
	media, ok := mediaModel(c, name)
	if !ok {
		return
	}
	r, _, err := s.mediaRunner(c.Request.Context(), name, CapabilitySpeech, nil)
	if err != nil {
		mediaRunnerError(c, err, name, CapabilitySpeech)
		return
	}
	resp, err := r.MediaDo(c.Request.Context(), http.MethodGet, "/props", nil, nil, "")
	if err != nil {
		mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
		return
	}
	defer resp.Body.Close()
	var props struct {
		Media struct {
			TTS engineTTS `json:"tts"`
		} `json:"media"`
	}
	if resp.StatusCode != http.StatusOK {
		mediaError(c, http.StatusBadGateway, fmt.Sprintf("media engine: /props answered %d", resp.StatusCode))
		return
	}
	if err := json.NewDecoder(resp.Body).Decode(&props); err != nil {
		mediaError(c, http.StatusBadGateway, "media engine: "+err.Error())
		return
	}
	c.JSON(http.StatusOK, mediaVoices(name, media.TTS, props.Media.TTS))
}

// engineTTS is the part of the engine's /props media.tts this reads. A voice
// is a name, or an object naming it (id or name), so a richer list from a
// later engine still reads.
type engineTTS struct {
	Voices          []json.RawMessage `json:"voices"`
	ResponseFormats []string          `json:"response_formats"`
	Formats         []string          `json:"formats"`
	SampleRate      int               `json:"sample_rate"`
}

func (e engineTTS) voiceNames() []string {
	var out []string
	for _, raw := range e.Voices {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			out = append(out, s)
			continue
		}
		var o struct{ ID, Name string }
		if json.Unmarshal(raw, &o) == nil {
			if o.ID != "" {
				out = append(out, o.ID)
			} else if o.Name != "" {
				out = append(out, o.Name)
			}
		}
	}
	return out
}

// mediaVoices merges the engine's voices with the template's names for them.
// A voice the template maps to but the engine does not list is kept: an
// engine that reports no voices still has the ones its template names.
func mediaVoices(name string, t *xollama.TTSMedia, e engineTTS) api.MediaVoicesResponse {
	out := api.MediaVoicesResponse{Model: name, SampleRate: e.SampleRate, ResponseFormats: e.ResponseFormats}
	if len(out.ResponseFormats) == 0 {
		out.ResponseFormats = e.Formats
	}
	ids := e.voiceNames()
	aliases := map[string][]string{}
	if t != nil {
		out.VoiceMap = t.VoiceMap
		for alias, id := range t.VoiceMap {
			aliases[id] = append(aliases[id], alias)
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if t.Defaults != nil {
			out.Default = t.Defaults.Voice
		}
	}
	if out.Default == "" && slices.Contains(ids, "default") {
		out.Default = "default"
	}
	if out.Default != "" && !slices.Contains(ids, out.Default) {
		ids = append(ids, out.Default)
	}
	sort.Strings(ids)
	out.Voices = make([]api.Voice, 0, len(ids))
	for _, id := range ids {
		a := aliases[id]
		sort.Strings(a)
		out.Voices = append(out.Voices, api.Voice{ID: id, Aliases: a})
	}
	return out
}
