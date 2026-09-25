package eino

import (
	"context"
	"io"
	"strings"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"councileval/council"
)

// streamMember is a researcher or critic whose output is an eino stream of
// {index: token} chunks. Invoked inside the graph, eino concatenates the
// chunks (per-key string concat of maps) into the same {index: text} the
// closure mode returns, and the node callbacks see a copy of the stream.
func streamMember(i int, role council.Role) func(context.Context, int) (*schema.StreamReader[map[int]string], error) {
	return func(ctx context.Context, round int) (*schema.StreamReader[map[int]string], error) {
		s, err := get(ctx)
		if err != nil {
			return nil, err
		}
		sr, sw := schema.Pipe[map[int]string](16)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer sw.Close()
			// The tokens are the node's output, so they must flow even when
			// deliberation is hidden; the forwarder drops them instead.
			cfg := s.cfg
			cfg.ShowDeliberation = true
			send := func(e council.Event) { sw.Send(map[int]string{i: e.Text}, nil) }
			var err error
			if role == council.Researcher {
				_, err = council.Research(ctx, s.m, cfg, s.d, s.conv, s.plan, i, round, s.critiques, send)
			} else {
				_, err = council.Critique(ctx, s.m, cfg, s.d, s.conv, s.plan, s.findings, i, round, send)
			}
			if err != nil {
				s.cancel(err)
				sw.Send(nil, err)
			}
		}()
		return sr, nil
	}
}

// forwarder is the per-run callback handler that turns the member streams
// eino copies into tagged thinking events.
func forwarder(j *job) callbacks.Handler {
	return callbacks.NewHandlerBuilder().OnEndWithStreamOutputFn(
		func(ctx context.Context, info *callbacks.RunInfo, out *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			var role council.Role
			switch {
			case strings.HasPrefix(info.Name, "researcher_"):
				role = council.Researcher
			case strings.HasPrefix(info.Name, "critic_"):
				role = council.Critic
			default:
				out.Close()
				return ctx
			}
			round := 0
			_ = compose.ProcessState(ctx, func(_ context.Context, st *state) error { round = st.round; return nil })
			j.wg.Add(1)
			j.drain.Add(1)
			go func() {
				defer j.wg.Done()
				defer j.drain.Done()
				defer out.Close()
				for {
					c, err := out.Recv()
					if err == io.EOF || err != nil {
						return
					}
					if !j.cfg.ShowDeliberation {
						continue
					}
					for i, t := range c.(map[int]string) {
						j.emit(council.Event{Role: role, Index: i, Round: round, Kind: council.Thinking, Text: t})
					}
				}
			}()
			return ctx
		}).Build()
}
