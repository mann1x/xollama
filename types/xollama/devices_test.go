package xollama

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestDevicePinsThatCannotBeServedAreRefused(t *testing.T) {
	for _, tt := range []struct {
		name, in, wantErr string
	}{
		{"ids without a backend", `{"version":3,"devices":{"ids":["0000:01:00.0"]}}`, "needs devices.backend"},
		{"unknown backend", `{"version":3,"devices":{"backend":"opencl"}}`, `unknown devices.backend "opencl"`},
		{"cpu has no devices", `{"version":3,"devices":{"backend":"CPU","ids":["0"]}}`, "nothing to select on the CPU"},
		{"duplicate", `{"version":3,"devices":{"backend":"CUDA","ids":["0","0"]}}`, `lists "0" twice`},
		{"duplicate across spellings", `{"version":3,"devices":{"backend":"CUDA","ids":["01:00.0","0000:01:00.0"]}}`, "twice"},
		{"empty id", `{"version":3,"devices":{"backend":"CUDA","ids":[""]}}`, "empty entry"},
		{"garbage", `{"version":3,"devices":{"backend":"CUDA","ids":["the big one"]}}`, "is not a PCI ID"},
		{"negative index", `{"version":3,"devices":{"backend":"CUDA","ids":["-1"]}}`, "is not a PCI ID"},
		{"too old a version", `{"version":2,"devices":{"backend":"CUDA"}}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse(%s) = %v, want success", tt.in, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse(%s) = %v, want error containing %q", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestHandWrittenDeviceSpellingsAreReadAsMeant(t *testing.T) {
	c, err := Parse([]byte(`{"version":3,"devices":{"backend":"vulkan","ids":["18:00.0","Integrated","2"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Devices.Backend != "Vulkan" {
		t.Errorf("backend = %q, want Vulkan", c.Devices.Backend)
	}
	if want := []string{"0000:18:00.0", "integrated", "2"}; !slices.Equal(c.Devices.IDs, want) {
		t.Errorf("ids = %q, want %q", c.Devices.IDs, want)
	}
}

func TestMarshalWritesTheCanonicalDevicePin(t *testing.T) {
	in := Config{Devices: &Devices{Backend: "cuda", IDs: []string{"01:00.0"}}}
	data, err := in.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Version != 3 || out.Devices.Backend != "CUDA" || out.Devices.IDs[0] != "0000:01:00.0" {
		t.Fatalf("marshalled %s", data)
	}
	if in.Devices.IDs[0] != "01:00.0" {
		t.Error("Marshal must not rewrite the caller's config")
	}
}

func TestADevicePinIsWorthStoring(t *testing.T) {
	if (&Config{Devices: &Devices{Backend: "CPU"}}).IsZero() {
		t.Error("a CPU pin says something and must be stored")
	}
	if !(&Config{Devices: &Devices{}}).IsZero() {
		t.Error("an empty devices object says nothing")
	}
}
