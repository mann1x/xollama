package xollama

import (
	"strings"
	"testing"
)

func TestValidatePoolCells(t *testing.T) {
	off := false
	for _, tt := range []struct {
		name    string
		c       Config
		wantErr string
	}{
		{"a pool on opencoti", Config{Version: 1, KV: &KV{PoolCells: 655360}}, ""},
		{"a pool pinned to opencoti", Config{Version: 1, Engine: EngineOpencoti, KV: &KV{PoolCells: 655360}}, ""},
		{"negative", Config{Version: 1, KV: &KV{PoolCells: -1}}, "must not be negative"},
		{"stock llama.cpp splits -c between slots", Config{Version: 1, Engine: EngineLlamaCpp, KV: &KV{PoolCells: 655360}}, "needs the opencoti engine"},
		{"a fixed split has no shared pool", Config{Version: 1, KV: &KV{PoolCells: 655360, Unified: &off}}, "needs kv.unified"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.c.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// A model stating only its pool states something, and it stays readable by
// an older build: an older build ignores it and serves num_ctx x num_parallel.
func TestAPoolAloneIsAStatedSettingAtTheBaseVersion(t *testing.T) {
	c := &Config{KV: &KV{PoolCells: 655360}}
	if c.IsZero() {
		t.Fatal("a config stating kv.pool_cells reads as empty")
	}
	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != SchemaVersionBase || got.KV.PoolCells != 655360 {
		t.Fatalf("round trip: version %d pool %d, want %d and 655360", got.Version, got.KV.PoolCells, SchemaVersionBase)
	}
}
