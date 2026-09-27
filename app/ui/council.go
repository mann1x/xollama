package ui

import (
	"github.com/ollama/ollama/api"
)

// councilThink keeps a council's explicit think:false. buildChatRequest sends
// Think only when it asks for thinking, which for an ordinary model is the same
// as leaving it out. For a council it is not: false is the Deliberation
// toggle's "off", and the council then sends the answer alone
// (docs/xollama/council.mdx). Every other request is left as built.
func councilThink(details *api.ShowResponse, think any, req *api.ChatRequest) {
	if req == nil || details == nil || details.Xollama == nil || !details.Xollama.Council.On() {
		return
	}
	if b, ok := think.(bool); ok && !b {
		req.Think = &api.ThinkValue{Value: false}
	}
}
