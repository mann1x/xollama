package tweak

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/types/xollama"
)

// gpuCommand is `xollama tweak server gpu`: which GPUs the server may use,
// in what order, through which backend, at what link speed, and whether a
// model is split across them (plans/system-settings.md).
func gpuCommand(opts Options) *cobra.Command {
	gpuCmd := &cobra.Command{
		Use:   "gpu [PCI-ID]",
		Short: "Set which GPUs the server uses, their priority, backend, link speed and split",
		Long: `Set the server's GPU policy.

    xollama tweak server gpu                                ask
    xollama tweak server gpu 0000:01:00.0 --priority 10     first choice
    xollama tweak server gpu 0000:02:00.0 --disable         keep models off it
    xollama tweak server gpu 0000:01:00.0 --backend CUDA    where it is also a Vulkan device
    xollama tweak server gpu 0000:01:00.0 --link 4x16       plan with PCIe 4.0 x16 (25.6 GB/s)
    xollama tweak server gpu 0000:01:00.0 --link auto       let the engine probe it again
    xollama tweak server gpu --split single                 never split a model across GPUs
    xollama tweak server gpu --split spread --split-mode row

Priority is fill order: a model that fits on one GPU goes to the
highest-priority GPU with room, and a split fills GPUs in that order. A model
whose own device pin (` + "`xollama tweak model --devices`" + `) names GPUs keeps them,
a disabled one included.

The link speed is what opencoti plans the KV rolling window with. It probes the
link when a model loads, and a host busy at that moment measures it wrong for
the whole session; a forced figure skips the probe. --link takes GB/s, a PCIe
generation and lane count (4x16, 5x8, ...), or auto.

--split is auto (split only a model too large for one GPU), spread (always) or
single (never: the rest of a model that does not fit stays on the CPU).
--split-mode is the engine's: layer (the default, validated on every opencoti
path) or row (CUDA only and unvalidated there; on Vulkan it becomes layer).

Changes apply at a model's next load; the command offers to unload the
running models. Only from the server's own machine.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGPU(cmd, args, opts)
		},
	}
	f := gpuCmd.Flags()
	f.Bool("disable", false, "Keep models off this GPU")
	f.Bool("enable", false, "Let models use this GPU again")
	f.String("priority", "", "Fill order: higher first (N|unset)")
	f.String("backend", "", "Backend for a GPU reachable through several (CUDA|Vulkan|ROCm|unset)")
	f.String("link", "", "Force the link speed: GB/s, GENxLANES (4x16), or auto")
	f.String("split", "", "auto|spread|single|unset")
	f.String("split-mode", "", "layer|row|unset")
	f.Bool("clear", false, "Remove the whole GPU policy")
	f.BoolP("yes", "y", false, "Unload running models without asking when a change needs it")
	return gpuCmd
}

// gpuEntry is one physical GPU: every backend it is reachable through.
type gpuEntry struct {
	PCI      string
	Name     string
	Total    uint64
	Backends []string
}

// physicalGPUs groups the server's devices by PCI ID. A device without one
// cannot be named in the policy, so it is left out.
func physicalGPUs(devs []api.XollamaDevice) []gpuEntry {
	var out []gpuEntry
	for _, d := range devs {
		pci, ok := xollama.CanonicalPCIID(d.PCIID)
		if !ok {
			continue
		}
		i := slices.IndexFunc(out, func(e gpuEntry) bool { return e.PCI == pci })
		if i < 0 {
			name := d.Description
			if name == "" {
				name = d.Name
			}
			out = append(out, gpuEntry{PCI: pci, Name: name, Total: d.TotalMemory})
			i = len(out) - 1
		}
		if !slices.Contains(out[i].Backends, d.Backend) {
			out[i].Backends = append(out[i].Backends, d.Backend)
		}
	}
	return out
}

func runGPU(cmd *cobra.Command, args []string, opts Options) error {
	if opts.Heartbeat != nil {
		if err := opts.Heartbeat(cmd, nil); err != nil {
			return err
		}
	}
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	a := newAsker(cmd.InOrStdin(), out)

	cur, err := client.Settings(ctx, nil)
	if err != nil {
		return err
	}
	policy := &xollama.GPUSettings{}
	if cur.GPU != nil {
		*policy = *cur.GPU
		policy.Devices = slices.Clone(cur.GPU.Devices)
	}
	var gpus []gpuEntry
	if devs, err := client.XollamaDevices(ctx); err == nil {
		gpus = physicalGPUs(devs.Devices)
	}

	if clear, _ := cmd.Flags().GetBool("clear"); clear {
		policy = &xollama.GPUSettings{}
	} else if cmd.Flags().NFlag() > countSet(cmd, "yes") || len(args) > 0 {
		if err := gpuFromFlags(cmd, args, policy); err != nil {
			return err
		}
	} else if err := askGPU(a, policy, gpus); err != nil {
		if errors.Is(err, errQuit) {
			fmt.Fprintf(out, "\nnothing changed.\n")
			return nil
		}
		return err
	}

	if err := policy.Validate(); err != nil {
		return err
	}
	resp, err := client.Settings(ctx, &api.SettingsRequest{GPU: policy})
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	printGPUTable(out, resp.GPU, gpus)
	yes, _ := cmd.Flags().GetBool("yes")
	return offerUnload(ctx, client, a, out, map[string]*string{"gpu": nil}, nil, yes)
}

// countSet counts the named flags that were given.
func countSet(cmd *cobra.Command, names ...string) int {
	n := 0
	for _, name := range names {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			n++
		}
	}
	return n
}

// gpuFromFlags applies the flags to the policy.
func gpuFromFlags(cmd *cobra.Command, args []string, g *xollama.GPUSettings) error {
	fl := cmd.Flags()
	if fl.Changed("split") {
		v, _ := fl.GetString("split")
		s, err := choice(v, xollama.ValidSplits())
		if err != nil {
			return fmt.Errorf("--split: %w", err)
		}
		g.SplitPolicy = s
	}
	if fl.Changed("split-mode") {
		v, _ := fl.GetString("split-mode")
		s, err := choice(v, xollama.ValidSplitModes())
		if err != nil {
			return fmt.Errorf("--split-mode: %w", err)
		}
		g.SplitMode = s
	}
	perDevice := countSet(cmd, "disable", "enable", "priority", "backend", "link") > 0
	if len(args) == 0 {
		if perDevice {
			return errors.New("--disable, --enable, --priority, --backend and --link need the GPU's PCI ID")
		}
		return nil
	}
	pci, ok := xollama.CanonicalPCIID(args[0])
	if !ok {
		return fmt.Errorf("%q is not a PCI ID (as `xollama tweak show server` lists them)", args[0])
	}
	d := device(g, pci)
	if fl.Changed("disable") && fl.Changed("enable") {
		return errors.New("--disable and --enable together")
	}
	if fl.Changed("disable") {
		d.Disabled = true
	}
	if fl.Changed("enable") {
		d.Disabled = false
	}
	if fl.Changed("priority") {
		v, _ := fl.GetString("priority")
		if v == "unset" {
			d.Priority = 0
		} else {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("--priority: want a whole number or unset (got %q)", v)
			}
			d.Priority = n
		}
	}
	if fl.Changed("backend") {
		v, _ := fl.GetString("backend")
		if v == "unset" {
			d.Backend = ""
		} else if b := xollama.CanonicalBackend(v); b != "" && b != "CPU" {
			d.Backend = b
		} else {
			return fmt.Errorf("--backend: want CUDA, Vulkan, ROCm or unset (got %q)", v)
		}
	}
	if fl.Changed("link") {
		v, _ := fl.GetString("link")
		gbps, err := parseLink(v)
		if err != nil {
			return fmt.Errorf("--link: %w", err)
		}
		d.LinkGBps = gbps
	}
	tidy(g)
	return nil
}

// device returns the policy's entry for pci, adding one when it has none.
func device(g *xollama.GPUSettings, pci string) *xollama.GPUDevice {
	for i := range g.Devices {
		if id, _ := xollama.CanonicalPCIID(g.Devices[i].ID); id == pci {
			return &g.Devices[i]
		}
	}
	g.Devices = append(g.Devices, xollama.GPUDevice{ID: pci})
	return &g.Devices[len(g.Devices)-1]
}

// tidy drops device entries that state nothing.
func tidy(g *xollama.GPUSettings) {
	g.Devices = slices.DeleteFunc(g.Devices, func(d xollama.GPUDevice) bool {
		return !d.Disabled && d.Priority == 0 && d.Backend == "" && d.LinkGBps == 0
	})
}

// PCIe generations: their transfer rate per lane in GT/s.
var pcieGT = map[string]float64{"3": 8, "3.0": 8, "4": 16, "4.0": 16, "5": 32, "5.0": 32, "6": 64, "6.0": 64}

var pcieLanes = []int{1, 2, 4, 8, 16}

// linkGBps is opencoti's own geometry formula, lanes x GT/s x 0.8 / 8, so a
// figure chosen here is exactly the one the engine would plan with.
func linkGBps(gt float64, lanes int) float64 {
	return float64(lanes) * gt * 0.8 / 8
}

// parseLink reads "auto", a GB/s figure, or GENxLANES ("4x16", "5.0x8").
func parseLink(v string) (float64, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "auto" || v == "unset" || v == "" {
		return 0, nil
	}
	if gen, lanes, ok := strings.Cut(v, "x"); ok {
		gt, ok := pcieGT[strings.TrimPrefix(gen, "gen")]
		n, err := strconv.Atoi(lanes)
		if !ok || err != nil || !slices.Contains(pcieLanes, n) {
			return 0, fmt.Errorf("want GENxLANES with GEN 3-6 and LANES 1, 2, 4, 8 or 16 (got %q)", v)
		}
		return linkGBps(gt, n), nil
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(v, "gb/s"), 64)
	if err != nil || f <= 0 || f > 1024 {
		return 0, fmt.Errorf("want GB/s, GENxLANES (4x16) or auto (got %q)", v)
	}
	return f, nil
}

// askGPU walks the operator through the policy until done.
func askGPU(a *asker, g *xollama.GPUSettings, gpus []gpuEntry) error {
	for {
		a.printf("\n")
		printGPUTable(a.out, g, gpus)
		options := [][2]string{{"done", "done: write it"}}
		for _, e := range gpus {
			options = append(options, [2]string{e.PCI, fmt.Sprintf("%s %s", e.PCI, e.Name)})
		}
		options = append(options,
			[2]string{"split", "split: when a model is spread over several GPUs"},
			[2]string{"split-mode", "split mode: how the engine splits it"},
			[2]string{"quit", "quit: change nothing"})
		choice, err := a.menu("gpu", "the server's GPUs", options, 0)
		if err != nil {
			return err
		}
		switch choice {
		case "done":
			return nil
		case "quit":
			return errQuit
		case "split":
			v, err := a.pick("split", "when a model is split across GPUs", g.SplitPolicy, [][2]string{
				{"auto", "auto: only a model too large for one GPU"},
				{"spread", "spread: always, over every allowed GPU"},
				{"single", "single: never; what does not fit stays on the CPU"},
			})
			if err != nil {
				return err
			}
			g.SplitPolicy = v
			if v == xollama.SplitSingle {
				g.SplitMode = ""
			}
		case "split-mode":
			if g.SplitPolicy == xollama.SplitSingle {
				a.printf("\n   skipped: split is single, so nothing is split.\n")
				continue
			}
			v, err := a.pick("split-mode", "how the engine splits a model", g.SplitMode, [][2]string{
				{"layer", "layer: whole layers per GPU, pipelined (the default; validated on every engine path)"},
				{"row", "row: weights split by rows, GPUs in parallel (CUDA only, unvalidated; Vulkan uses layer)"},
			})
			if err != nil {
				return err
			}
			g.SplitMode = v
		default:
			i := slices.IndexFunc(gpus, func(e gpuEntry) bool { return e.PCI == choice })
			if err := askDevice(a, g, gpus[i]); err != nil {
				return err
			}
		}
		tidy(g)
	}
}

// pick asks for one of options, or unset. It returns "" for unset.
func (a *asker) pick(name, title, current string, options [][2]string) (string, error) {
	def := len(options)
	for i, o := range options {
		if o[0] == current {
			def = i
		}
	}
	all := append(slices.Clone(options), [2]string{"unset", "unset: no setting, the default applies"})
	v, err := a.menu(name, title, all, def)
	if v == "unset" {
		v = ""
	}
	return v, err
}

// askDevice asks about one GPU.
func askDevice(a *asker, g *xollama.GPUSettings, e gpuEntry) error {
	d := device(g, e.PCI)
	use, err := a.pick("use", fmt.Sprintf("%s %s: may models use it?", e.PCI, e.Name), map[bool]string{false: "yes", true: "no"}[d.Disabled], [][2]string{
		{"yes", "yes"},
		{"no", "no: keep models off it, unless a model pins it"},
	})
	if err != nil {
		return err
	}
	d.Disabled = use == "no"
	if d.Disabled {
		return nil
	}

	a.printf("\npriority: higher fills first (now %d; empty keeps it)\npriority [%d]> ", d.Priority, d.Priority)
	raw, err := a.readLine()
	if err != nil {
		return err
	}
	a.answered++
	if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			a.printf("  ! want a whole number; priority kept\n")
		} else {
			d.Priority = n
		}
	}

	if len(e.Backends) > 1 {
		options := make([][2]string, 0, len(e.Backends))
		for _, b := range e.Backends {
			options = append(options, [2]string{b, b})
		}
		b, err := a.pick("backend", "this GPU is reachable through several backends; which one?", d.Backend, options)
		if err != nil {
			return err
		}
		d.Backend = b
	}

	return askLink(a, d)
}

// askLink is the link wizard: PCIe generation, then lanes, or a figure.
func askLink(a *asker, d *xollama.GPUDevice) error {
	now := "auto (the engine probes it)"
	if d.LinkGBps > 0 {
		now = fmt.Sprintf("forced to %g GB/s", d.LinkGBps)
	}
	how, err := a.menu("link", "link speed the engine plans the KV rolling window with: "+now, [][2]string{
		{"keep", "keep it"},
		{"auto", "auto: the engine probes it when a model loads"},
		{"pcie", "force a PCIe generation and lane count"},
		{"gbps", "force a GB/s figure"},
	}, 0)
	if err != nil {
		return err
	}
	switch how {
	case "auto":
		d.LinkGBps = 0
	case "pcie":
		// Word keys (gen4, x8): a bare number answers a menu by position.
		gen, err := a.menu("pcie-gen", "PCIe generation", [][2]string{{"gen3", "3.0"}, {"gen4", "4.0"}, {"gen5", "5.0"}, {"gen6", "6.0"}}, 1)
		if err != nil {
			return err
		}
		lanes, err := a.menu("pcie-lanes", "PCIe lanes", [][2]string{{"x1", "x1"}, {"x2", "x2"}, {"x4", "x4"}, {"x8", "x8"}, {"x16", "x16"}}, 4)
		if err != nil {
			return err
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(lanes, "x"))
		g := strings.TrimPrefix(gen, "gen")
		d.LinkGBps = linkGBps(pcieGT[g], n)
		a.printf("   PCIe %s.0 x%d = %g GB/s\n", g, n, d.LinkGBps)
	case "gbps":
		for {
			a.printf("\nGB/s\nlink-gbps []> ")
			raw, err := a.readLine()
			if err != nil {
				return err
			}
			a.answered++
			f, err := parseLink(raw)
			if err == nil && f > 0 {
				d.LinkGBps = f
				break
			}
			a.printf("  ! want a positive figure in GB/s\n")
		}
	}
	return nil
}

// printGPUTable lists the GPUs with their policy, and the split.
func printGPUTable(out io.Writer, g *xollama.GPUSettings, gpus []gpuEntry) {
	rows := [][]string{{"PCI ID", "GPU", "MEMORY", "BACKENDS", "USE", "PRIORITY", "BACKEND", "LINK"}}
	listed := map[string]bool{}
	row := func(pci, name, mem, backends string) {
		listed[pci] = true
		d, _ := g.Device(pci)
		use := "yes"
		if d.Disabled {
			use = "no"
		}
		backend, link := d.Backend, "auto"
		if backend == "" {
			backend = "-"
		}
		if d.LinkGBps > 0 {
			link = fmt.Sprintf("%g GB/s", d.LinkGBps)
		}
		rows = append(rows, []string{pci, name, mem, backends, use, strconv.Itoa(d.Priority), backend, link})
	}
	for _, e := range gpus {
		row(e.PCI, e.Name, format.HumanBytes2(e.Total), strings.Join(e.Backends, ","))
	}
	if g != nil {
		for _, d := range g.Devices {
			if pci, _ := xollama.CanonicalPCIID(d.ID); !listed[pci] {
				row(pci, "(not found now)", "-", "-")
			}
		}
	}
	if len(rows) > 1 {
		printRows(out, rows)
	} else {
		fmt.Fprintln(out, "no GPU with a PCI ID is visible to the server.")
	}
	split, mode := "auto (OLLAMA_SCHED_SPREAD decides)", "the engine's (layer)"
	if g != nil && g.SplitPolicy != "" {
		split = g.SplitPolicy
	}
	if g != nil && g.SplitMode != "" {
		mode = g.SplitMode
	}
	fmt.Fprintf(out, "split: %s; split mode: %s\n", split, mode)
}

// gpuPolicy reads the server's policy and its GPUs, for `tweak show server`.
func gpuPolicy(ctx context.Context, client *api.Client, resp *api.SettingsResponse) []gpuEntry {
	devs, err := client.XollamaDevices(ctx)
	if err != nil {
		return nil
	}
	return physicalGPUs(devs.Devices)
}
