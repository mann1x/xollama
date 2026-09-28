package server

// xollama: the council's resumable state (council_chat_state_v1) --
// plans/agentic-council-chat.md, Phase 9.4.
//
// A council turn that breaks off -- a dropped connection, a client restart --
// resumes from where it stopped when the client sends back the latest state it
// received. The state is a protobuf message, sealed with AES-256-GCM under a key
// only this server holds, and travels as base64: the client keeps it and sends
// it back, and never reads it. It carries:
//
//   - what the turn has finished (council.Progress): route, plan, each
//     member's reply, bound to a hash of the history and of the user turn in
//     flight, so it resumes only that turn;
//   - each member suspended on its tool calls (9.5): its own turns so far,
//     which the client's results resume;
//   - the conversation's compaction record, restored when this server's memory
//     has none or an older one (after a restart), and applied only where its
//     own hash matches the conversation.
//
// A missing, unreadable, foreign or mismatched state is a fresh start, never an
// error: the client may send one from before a rewind, an edit, a restart of
// ours with a lost key, or another server.
//
// Wire layout (field numbers are the contract; add, never renumber):
//
//	State     1 version(varint) 2 history(bytes) 3 turn(bytes) 4 progress(Progress) 5 record(Record)
//	          6 kept(Progress) 7 kept_n(varint) 8 kept_prefix(bytes)  -- the deliberation an
//	          answered turn leaves for the next (council_continue.go)
//	Progress  1 route(string) 2 plan(Plan) 3 rounds(Round, repeated) 4 suspended(Member, repeated)
//	          5 notes(Note, repeated) 6 seen(Seen, repeated) 7 tests(string, repeated)
//	          8 replans(Plan, repeated)
//	Note      1 id(string) 2 from(string) 3 text(string)
//	Seen      1 key(string) 2 n(varint)
//	Member    1 key(string) 2 turns(bytes: the member's []api.Message as JSON)
//	Plan      1 plan(string) 2 briefs(string, repeated)
//	Round     1 findings(string, repeated) 2 critiques(string, repeated)
//	Record    1 n(varint) 2 hash(string) 3 head(Message, repeated) 4 gen(varint)
//	          5 requests(string, repeated) 6 retro(string) 7 replay(string) 8 how(string)
//	Message   1 role(string) 2 content(string)

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/internal/council"
	"github.com/ollama/ollama/internal/fsowner"
)

const (
	councilStateVersion = 1
	councilStateSeal    = 1 // the envelope's first byte: AES-256-GCM, 12-byte nonce
	councilStateKeyFile = "council-state.key"
)

// councilStateAAD binds a sealed state to its purpose: a key reused for
// anything else cannot open it.
var councilStateAAD = []byte("xollama council_chat_state v1")

// councilState is one turn's resumable state.
type councilState struct {
	history, turn []byte // sha256 of the history before the user turn, and of that turn
	progress      council.Progress
	record        *compactionRecord
	kept          *keptTurn
}

// councilTurnHashes binds a state to a request: the history before the last
// user message, and that message. Only role and content count: a client that
// re-sends the same turns with other fields set is still the same turn.
func councilTurnHashes(msgs []api.Message) (history, turn []byte) {
	last := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			last = i
			break
		}
	}
	h := func(ms []api.Message) []byte {
		type rc struct{ R, C string }
		out := make([]rc, len(ms))
		for i, m := range ms {
			out[i] = rc{m.Role, m.Content}
		}
		b, _ := json.Marshal(out)
		s := sha256.Sum256(b)
		return s[:]
	}
	if last < 0 {
		return h(msgs), h(nil)
	}
	return h(msgs[:last]), h(msgs[last : last+1])
}

func (s councilState) marshal() []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, councilStateVersion)
	b = appendBytes(b, 2, s.history)
	b = appendBytes(b, 3, s.turn)
	b = appendBytes(b, 4, marshalProgress(s.progress))
	if s.record != nil {
		b = appendBytes(b, 5, marshalRecord(s.record))
	}
	if s.kept != nil {
		b = appendBytes(b, 6, marshalProgress(s.kept.p))
		b = protowire.AppendTag(b, 7, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(s.kept.n))
		b = appendBytes(b, 8, s.kept.prefix)
	}
	return b
}

func marshalProgress(p council.Progress) []byte {
	var b []byte
	b = appendString(b, 1, p.Route)
	if p.Plan != nil {
		b = appendBytes(b, 2, marshalPlan(*p.Plan))
	}
	for _, r := range p.Rounds {
		var rb []byte
		for _, f := range r.Findings {
			rb = appendRepeated(rb, 1, f)
		}
		for _, c := range r.Critiques {
			rb = appendRepeated(rb, 2, c)
		}
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendBytes(b, rb)
	}
	for _, k := range slices.Sorted(maps.Keys(p.Suspended)) {
		turns, err := json.Marshal(p.Suspended[k])
		if err != nil {
			continue // unreachable for api.Message; a member left out starts over
		}
		var mb []byte
		mb = appendString(mb, 1, k)
		mb = appendBytes(mb, 2, turns)
		b = appendBytes(b, 4, mb)
	}
	for _, n := range p.Notes {
		var nb []byte
		nb = appendString(nb, 1, n.ID)
		nb = appendString(nb, 2, n.From)
		nb = appendString(nb, 3, n.Text)
		b = appendBytes(b, 5, nb)
	}
	for _, k := range slices.Sorted(maps.Keys(p.Seen)) {
		var sb []byte
		sb = appendString(sb, 1, k)
		sb = protowire.AppendTag(sb, 2, protowire.VarintType)
		sb = protowire.AppendVarint(sb, uint64(p.Seen[k]))
		b = appendBytes(b, 6, sb)
	}
	for _, t := range p.Tests {
		b = appendRepeated(b, 7, t)
	}
	for _, pl := range p.Replans {
		b = appendBytes(b, 8, marshalPlan(pl))
	}
	return b
}

func marshalPlan(p council.Plan) []byte {
	var b []byte
	b = appendString(b, 1, p.Plan)
	for _, br := range p.Briefs {
		b = appendRepeated(b, 2, br)
	}
	return b
}

func unmarshalPlan(v []byte) (council.Plan, error) {
	var pl council.Plan
	err := fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		switch {
		case num == 1 && typ == protowire.BytesType:
			pl.Plan = string(v)
		case num == 2 && typ == protowire.BytesType:
			pl.Briefs = append(pl.Briefs, string(v))
		}
		return nil
	})
	return pl, err
}

func marshalRecord(r *compactionRecord) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(r.n))
	b = appendString(b, 2, r.hash)
	for _, m := range r.head {
		var mb []byte
		mb = appendString(mb, 1, m.Role)
		mb = appendString(mb, 2, m.Content)
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendBytes(b, mb)
	}
	b = protowire.AppendTag(b, 4, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(r.gen))
	for _, q := range r.requests {
		b = appendRepeated(b, 5, q)
	}
	b = appendString(b, 6, r.retro)
	b = appendString(b, 7, r.replay)
	b = appendString(b, 8, r.how)
	return b
}

// appendString writes a singular string, which proto3 leaves out when empty.
func appendString(b []byte, num protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	return appendRepeated(b, num, s)
}

// appendRepeated writes one element of a repeated string, empty ones too:
// an empty slot is a member not yet done, and its position is its index.
func appendRepeated(b []byte, num protowire.Number, s string) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendBytes(b []byte, num protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

var errCouncilState = errors.New("council_chat_state: malformed")

// fields walks one message, calling f with each field; unknown fields are
// skipped, as protobuf requires, so a newer state still opens here.
func fields(b []byte, f func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error) error {
	for len(b) > 0 {
		num, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return errCouncilState
		}
		b = b[l:]
		var v []byte
		var n uint64
		switch typ {
		case protowire.VarintType:
			n, l = protowire.ConsumeVarint(b)
		case protowire.BytesType:
			v, l = protowire.ConsumeBytes(b)
		default:
			l = protowire.ConsumeFieldValue(num, typ, b)
		}
		if l < 0 {
			return errCouncilState
		}
		b = b[l:]
		if err := f(num, typ, v, n); err != nil {
			return err
		}
	}
	return nil
}

func unmarshalCouncilState(b []byte) (councilState, error) {
	var s councilState
	version := uint64(0)
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
		switch {
		case num == 1 && typ == protowire.VarintType:
			version = n
		case num == 2 && typ == protowire.BytesType:
			s.history = append([]byte(nil), v...)
		case num == 3 && typ == protowire.BytesType:
			s.turn = append([]byte(nil), v...)
		case num == 4 && typ == protowire.BytesType:
			p, err := unmarshalProgress(v)
			s.progress = p
			return err
		case num == 5 && typ == protowire.BytesType:
			r, err := unmarshalRecord(v)
			s.record = r
			return err
		case num == 6 && typ == protowire.BytesType:
			p, err := unmarshalProgress(v)
			if s.kept == nil {
				s.kept = &keptTurn{}
			}
			s.kept.p = p
			return err
		case num == 7 && typ == protowire.VarintType:
			if s.kept == nil {
				s.kept = &keptTurn{}
			}
			s.kept.n = int(n)
		case num == 8 && typ == protowire.BytesType:
			if s.kept == nil {
				s.kept = &keptTurn{}
			}
			s.kept.prefix = append([]byte(nil), v...)
		}
		return nil
	})
	if err == nil && version != councilStateVersion {
		err = errors.New("council_chat_state: unknown version")
	}
	return s, err
}

func unmarshalProgress(b []byte) (council.Progress, error) {
	var p council.Progress
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case 1:
			p.Route = string(v)
		case 2:
			pl, err := unmarshalPlan(v)
			if err != nil {
				return err
			}
			p.Plan = &pl
		case 3:
			var r council.RoundProgress
			if err := fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
				switch {
				case num == 1 && typ == protowire.BytesType:
					r.Findings = append(r.Findings, string(v))
				case num == 2 && typ == protowire.BytesType:
					r.Critiques = append(r.Critiques, string(v))
				}
				return nil
			}); err != nil {
				return err
			}
			p.Rounds = append(p.Rounds, r)
		case 4:
			var key string
			var turns []api.Message
			if err := fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
				switch {
				case num == 1 && typ == protowire.BytesType:
					key = string(v)
				case num == 2 && typ == protowire.BytesType:
					return json.Unmarshal(v, &turns)
				}
				return nil
			}); err != nil {
				return err
			}
			if key != "" && len(turns) > 0 {
				if p.Suspended == nil {
					p.Suspended = map[string][]api.Message{}
				}
				p.Suspended[key] = turns
			}
		case 5:
			var n council.Note
			if err := fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
				if typ == protowire.BytesType {
					switch num {
					case 1:
						n.ID = string(v)
					case 2:
						n.From = string(v)
					case 3:
						n.Text = string(v)
					}
				}
				return nil
			}); err != nil {
				return err
			}
			p.Notes = append(p.Notes, n)
		case 6:
			var key string
			var seen uint64
			if err := fields(v, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
				switch {
				case num == 1 && typ == protowire.BytesType:
					key = string(v)
				case num == 2 && typ == protowire.VarintType:
					seen = n
				}
				return nil
			}); err != nil {
				return err
			}
			if key != "" {
				if p.Seen == nil {
					p.Seen = map[string]int{}
				}
				p.Seen[key] = int(seen)
			}
		case 7:
			p.Tests = append(p.Tests, string(v))
		case 8:
			pl, err := unmarshalPlan(v)
			if err != nil {
				return err
			}
			p.Replans = append(p.Replans, pl)
		}
		return nil
	})
	return p, err
}

func unmarshalRecord(b []byte) (*compactionRecord, error) {
	r := &compactionRecord{}
	err := fields(b, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
		switch {
		case num == 1 && typ == protowire.VarintType:
			r.n = int(n)
		case num == 2 && typ == protowire.BytesType:
			r.hash = string(v)
		case num == 3 && typ == protowire.BytesType:
			var m api.Message
			if err := fields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
				switch {
				case num == 1 && typ == protowire.BytesType:
					m.Role = string(v)
				case num == 2 && typ == protowire.BytesType:
					m.Content = string(v)
				}
				return nil
			}); err != nil {
				return err
			}
			r.head = append(r.head, m)
		case num == 4 && typ == protowire.VarintType:
			r.gen = int(n)
		case num == 5 && typ == protowire.BytesType:
			r.requests = append(r.requests, string(v))
		case num == 6 && typ == protowire.BytesType:
			r.retro = string(v)
		case num == 7 && typ == protowire.BytesType:
			r.replay = string(v)
		case num == 8 && typ == protowire.BytesType:
			r.how = string(v)
		}
		return nil
	})
	return r, err
}

// sealCouncilState is the blob a client carries: the seal byte, the nonce and
// the ciphertext of the state, in base64.
func sealCouncilState(s councilState) (string, error) {
	aead, err := councilStateAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append([]byte{councilStateSeal}, nonce...)
	out = aead.Seal(out, nonce, s.marshal(), councilStateAAD)
	return base64.StdEncoding.EncodeToString(out), nil
}

// openCouncilState opens a blob this server sealed. Anything else -- a blob
// from another server, a lost key, a changed byte -- is an error the caller
// treats as no state at all.
func openCouncilState(blob string) (councilState, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return councilState{}, err
	}
	aead, err := councilStateAEAD()
	if err != nil {
		return councilState{}, err
	}
	if len(raw) < 1+aead.NonceSize() || raw[0] != councilStateSeal {
		return councilState{}, errors.New("council_chat_state: not a sealed state")
	}
	nonce, sealed := raw[1:1+aead.NonceSize()], raw[1+aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, sealed, councilStateAAD)
	if err != nil {
		return councilState{}, err
	}
	return unmarshalCouncilState(plain)
}

// councilStateKeyPath is where the key is kept; tests point it elsewhere.
var councilStateKeyPath = func() string { return filepath.Join(envconfig.Models(), councilStateKeyFile) }

var councilStateKey struct {
	once sync.Once
	aead cipher.AEAD
	err  error
}

// councilStateAEAD is the server's sealing key, kept in the model store so a
// state survives a restart -- that is when a compaction record is restored
// from one. Written through fsowner, so a root-run server leaves it to the
// store's owner. A key that can be neither read nor written is made for this
// process alone: states then open until the next restart, and fail as no
// state after it.
func councilStateAEAD() (cipher.AEAD, error) {
	councilStateKey.once.Do(func() {
		key, err := loadCouncilStateKey(councilStateKeyPath())
		if err != nil {
			slog.Warn("council: the state key cannot be kept in the model store; states will not survive a restart", "error", err)
			key = make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				councilStateKey.err = err
				return
			}
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			councilStateKey.err = err
			return
		}
		councilStateKey.aead, councilStateKey.err = cipher.NewGCM(block)
	})
	return councilStateKey.aead, councilStateKey.err
}

func loadCouncilStateKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != 32 {
			return nil, errors.New("council state key: not 32 bytes")
		}
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := fsowner.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := fsowner.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
