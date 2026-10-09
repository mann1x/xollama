package llm

// appendDraftOffArgs tells opencoti not to draft when someone turned a
// model's drafter off.
//
// Upstream's launch says "off" by passing no draft arguments, which is the
// whole of it on stock llama.cpp. opencoti selects a driver on its own for a
// model that carries a NextN head: started with nothing, it drafts
// (oc_auto_select_spec_type), so a draft length of 0 used to change nothing
// there. --spec-type none is the engine's off, and it also leaves the head
// out of memory.
//
// Measured on c10 r3 (2610090401001), Qwen3.6-27B with a built-in head, RTX
// 3090, 256 tokens: nothing passed 33.3 and 31.9 tok/s with the head loaded
// (276 MiB); --auto-mtp-policy off 23.9 tok/s, head still loaded;
// --spec-type none 24.1 and 24.0 tok/s, head not loaded, 722 MiB less on the
// card (/srv/ml/xc10/drafter/builtin.out).
//
// off is the model's own word (server.draftTurnedOff: a PARAMETER
// draft_num_predict of 0, or draft.tokens 0) with no request asking for a
// length. A model nobody configured is left to the engine, as before.
func appendDraftOffArgs(args []string, draftType string, off, usedOpencoti bool) []string {
	if !usedOpencoti || draftType == "" || !off {
		return args
	}
	return append(args, "--spec-type", "none")
}
