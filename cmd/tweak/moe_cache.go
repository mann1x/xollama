package tweak

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ollama/ollama/api"
)

// forgetMoEProfile asks the server to delete the model's MoE cache
// warm-start profile (the server unloads the model first: the engine writes
// the profile as it exits).
func forgetMoEProfile(cmd *cobra.Command, client *api.Client, name string) error {
	resp, err := client.ForgetMoECacheProfile(cmd.Context(), name)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if resp.Unloaded {
		fmt.Fprintf(out, "unloaded %s first\n", name)
	}
	if len(resp.Removed) == 0 {
		fmt.Fprintf(out, "%s has no MoE cache warm-start profile (key %s); nothing to forget\n", name, resp.Key)
	} else {
		fmt.Fprintf(out, "forgot the MoE cache warm-start profile of %s: %s\n", name, strings.Join(resp.Removed, ", "))
	}
	if resp.Note != "" {
		fmt.Fprintf(out, "note: %s\n", resp.Note)
	}
	return nil
}

// anyFieldFlag reports whether the command names a setting.
func anyFieldFlag(cmd *cobra.Command) bool {
	for _, f := range fields {
		if cmd.Flags().Changed(f.name) {
			return true
		}
	}
	for _, n := range []string{"clear", "json", "dry-run"} {
		if cmd.Flags().Changed(n) {
			return true
		}
	}
	return false
}

// MoECacheText is one line for `xollama ps`: what the engine did with the
// MoE cache on a device. Empty for a device where there is nothing to say
// (no cache asked for and none made because none was needed).
func MoECacheText(d api.MoECacheDevice) string {
	var b strings.Builder
	switch d.Verdict {
	case "pays":
		b.WriteString("on (automatic: measured to pay)")
	case "explicit":
		b.WriteString("on (fixed size)")
	case "would_not_pay":
		b.WriteString("off (automatic: measured not to pay)")
	case "probe_failed":
		b.WriteString("off (automatic: the measurement could not run)")
	case "multi_gpu":
		b.WriteString("off (automatic sizing is for one GPU; set a size with tweak model --moe-cache)")
	case "off":
		return ""
	default:
		b.WriteString(d.Verdict)
	}
	if d.CacheMiB != nil && *d.CacheMiB > 0 {
		fmt.Fprintf(&b, ", %d MiB", *d.CacheMiB)
		if d.HostExpertMiB != nil {
			fmt.Fprintf(&b, " for %d MiB of experts in RAM", *d.HostExpertMiB)
		}
	}
	if d.LayersRAMFit != nil && d.LayersRAMFinal != nil && *d.LayersRAMFinal != *d.LayersRAMFit {
		fmt.Fprintf(&b, ", %d expert layers in RAM (the fit alone: %d)", *d.LayersRAMFinal, *d.LayersRAMFit)
	}
	if p := d.Profile; p != nil && p.Read && p.WarmExperts != nil {
		fmt.Fprintf(&b, ", warm start %d experts", *p.WarmExperts)
	}
	if d.Reason != nil && *d.Reason != "" && d.Verdict != "pays" && d.Verdict != "explicit" {
		fmt.Fprintf(&b, " — %s", *d.Reason)
	}
	return b.String()
}
