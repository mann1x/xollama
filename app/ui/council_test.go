package ui

import (
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/xollama"
)

func councilShow(on bool) *api.ShowResponse {
	return &api.ShowResponse{Xollama: &xollama.Config{Council: &xollama.Council{Enabled: &on}}}
}

// The Deliberation toggle's "off" must reach a council as think:false; the
// desktop backend otherwise drops every false.
func TestACouncilKeepsThinkFalse(t *testing.T) {
	req := &api.ChatRequest{}
	councilThink(councilShow(true), false, req)
	if req.Think == nil || req.Think.Bool() {
		t.Fatalf("think = %+v, want an explicit false", req.Think)
	}
}

func TestAnOrdinaryModelsThinkIsUpstreams(t *testing.T) {
	for name, details := range map[string]*api.ShowResponse{
		"no xollama block": {},
		"council off":      councilShow(false),
		"no details":       nil,
	} {
		req := &api.ChatRequest{}
		councilThink(details, false, req)
		if req.Think != nil {
			t.Errorf("%s: think = %+v, want it left out as upstream does", name, req.Think)
		}
	}
	// A council asked to think, or given nothing, keeps what was built.
	for _, think := range []any{true, nil, "high"} {
		req := &api.ChatRequest{}
		councilThink(councilShow(true), think, req)
		if req.Think != nil {
			t.Errorf("think %v: got %+v, want the request as built", think, req.Think)
		}
	}
}
