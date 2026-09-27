package cmd

import (
	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
)

// xollama-hook: council — `xollama run <council> "prompt"`.
//
// A council answers chat turns: its hook is in ChatHandler, because a council
// is a conversation between members, and /api/generate's raw prompt, suffix
// and context have no place in one. Upstream's `run` sends a one-shot prompt
// to /api/generate, so on a council model it got the plain model, with no
// deliberation and no error to say so. For a council the prompt is sent as
// the one user turn of a chat instead, which is what interactive `run`
// already does.

// runsAsCouncil reports whether a one-shot prompt on this model must go
// through chat. A requested format is left to generate: the council steps
// aside for it either way.
func runsAsCouncil(info *api.ShowResponse, opts runOptions) bool {
	return info != nil && info.Xollama != nil && info.Xollama.Council.On() && opts.Format == ""
}

// runCouncilOnce sends the prompt as a chat turn, with the system prompt the
// flags gave, and prints it as chat() does for an interactive turn.
func runCouncilOnce(cmd *cobra.Command, opts runOptions) error {
	var msgs []api.Message
	if opts.System != "" {
		msgs = append(msgs, api.Message{Role: "system", Content: opts.System})
	}
	opts.Messages = append(msgs, api.Message{Role: "user", Content: opts.Prompt, Images: opts.Images})
	_, err := chat(cmd, opts)
	return err
}
