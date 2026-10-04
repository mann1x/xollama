package xollama

import (
	"slices"
	"strings"
	"testing"
)

func TestADeviceIsNamedByWhatItIsCalled(t *testing.T) {
	for _, tt := range []struct {
		in, name string
		ordinal  int
		ok       bool
	}{
		{"name:AMD Radeon RX 9070 XT", "AMD Radeon RX 9070 XT", 0, true},
		{"NAME:  AMD   Radeon RX 9070 XT #2", "AMD Radeon RX 9070 XT", 2, true},
		{"name:Card #x", "Card #x", 0, true},
		{"name:Card#0", "Card#0", 0, true},
		{"name:", "", 0, false},
		{"name:#2", "", 2, false},
		{"0000:01:00.0", "", 0, false},
		{"2", "", 0, false},
	} {
		name, ordinal, ok := ParseNameSelector(tt.in)
		if name != tt.name || ordinal != tt.ordinal || ok != tt.ok {
			t.Errorf("ParseNameSelector(%q) = %q, %d, %v; want %q, %d, %v", tt.in, name, ordinal, ok, tt.name, tt.ordinal, tt.ok)
		}
	}
	if got := NameSelector(" AMD  Radeon RX 9070 XT ", 2); got != "name:AMD Radeon RX 9070 XT#2" {
		t.Errorf("NameSelector = %q", got)
	}
	if !NameSelects("amd radeon rx 9070 xt", 0, "AMD Radeon RX 9070 XT", 3) {
		t.Error("a name without a place does not select every device of that name")
	}
	if NameSelects("AMD Radeon RX 9070 XT", 2, "AMD Radeon RX 9070 XT", 1) || !NameSelects("AMD Radeon RX 9070 XT", 2, "AMD Radeon RX 9070 XT", 2) {
		t.Error("#2 does not select the second of its name, and only it")
	}
	if NameSelects("AMD Radeon RX 9070 XT", 0, "AMD Radeon(TM) Graphics", 1) {
		t.Error("a name selects a device of another name")
	}
}

func TestAModelPinsADeviceByName(t *testing.T) {
	c, err := Parse([]byte(`{"version":3,"devices":{"backend":"vulkan","ids":["NAME: AMD Radeon RX  9070 XT","name:NVIDIA GeForce RTX 3090#2"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"name:AMD Radeon RX 9070 XT", "name:NVIDIA GeForce RTX 3090#2"}; !slices.Equal(c.Devices.IDs, want) {
		t.Errorf("ids = %q, want %q", c.Devices.IDs, want)
	}
	_, err = Parse([]byte(`{"version":3,"devices":{"backend":"Vulkan","ids":["name:AMD Radeon RX 9070 XT","name:amd radeon rx 9070 xt"]}}`))
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("one name in two spellings: %v, want a refusal", err)
	}
	if !IsPositional("2") || IsPositional("name:2") || IsPositional("0000:01:00.0") || IsPositional("integrated") {
		t.Error("IsPositional is true for a bare index only")
	}
}

func TestTheGPUPolicyKeysAGPUByNameWhereItHasNoPCIID(t *testing.T) {
	g := &GPUSettings{Devices: []GPUDevice{
		{ID: "0000:11:00.0", Priority: 1},
		{ID: "name:AMD Radeon RX 9070 XT", Priority: 9},
		{ID: "name:Twin#2", Disabled: true},
	}}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	if d, ok := g.Lookup("", "amd radeon rx 9070 xt", 1); !ok || d.Priority != 9 {
		t.Errorf("by name: %+v %v", d, ok)
	}
	if d, ok := g.Lookup("0000:11:00.0", "AMD Radeon RX 9070 XT", 1); !ok || d.Priority != 1 {
		t.Errorf("a PCI ID is looked up before a name: %+v %v", d, ok)
	}
	if _, ok := g.Lookup("", "Twin", 1); ok {
		t.Error("the first Twin got the second's settings")
	}
	if d, ok := g.Lookup("", "Twin", 2); !ok || !d.Disabled {
		t.Error("the second Twin is not found")
	}
	if d, ok := g.Device("NAME:amd radeon rx 9070 xt"); !ok || d.Priority != 9 {
		t.Error("Device does not find a name key in another spelling")
	}

	for _, bad := range []*GPUSettings{
		{Devices: []GPUDevice{{ID: "name:X", LinkGBps: 12.8}}},
		{Devices: []GPUDevice{{ID: "name:X", Priority: 1}, {ID: "NAME:x", Priority: 2}}},
		{Devices: []GPUDevice{{ID: "2", Priority: 1}}},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v was accepted", bad.Devices)
		}
	}
}
