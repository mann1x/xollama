package server

// xollama: the council's compaction prompts -- plans/agentic-council-chat.md,
// Phase 8. Ported from Cerebriline (/shared/dev/cline, sdk/packages/core/src/
// extensions/context/): replay-compaction.ts, full-compaction.ts,
// council-compaction.ts, compaction-shared.ts and agentic-compaction.ts. The
// originals are written for an agent that calls tools, with a numbered record
// of its calls to cite; a council answers chat turns and calls none, so the
// citation and tool paragraphs are gone and the rest speaks of a conversation.
// Keep the structure and the rules; change the wording only against a
// measurement.

// The system text each compaction role runs under (SUMMARIZER_SYSTEM_PROMPTS,
// COUNCIL_SYSTEM_PROMPTS). The writer and the critics continue the
// conversation itself, so theirs goes into their instruction instead of
// replacing the conversation's system message.
const (
	compactionTailRole = "You are re-telling your own recent part in this conversation in your own voice, because the earlier part of it is about to be discarded and what you write takes its place directly in front of the turns that remain. Match the prose of those turns. Keep every specific — names, quoted wording, figures, errors, decisions — that you would otherwise have to rediscover."

	compactionFullRole = "You write the state record for a conversation. The conversation is about to be discarded, so what you write is the only record that remains. Follow the requested structure exactly, section for section, and keep every specific — names, quoted wording, figures, errors, decisions — that whoever continues would otherwise have to rediscover."

	compactionRetrospectiveRole = "You are assessing your own reasoning in a conversation that is about to be discarded. This is judgement, not a record — what happened is written up separately. Be terse, and say only what you would want to know before the next hour of this conversation."

	compactionCriticRole = "You are rewriting one half of an account of your own part in a conversation, with the conversation in front of you. Correct what the conversation contradicts, add what it shows missing, fix every quotation against it, and improve the prose where it has drifted out of voice. You return your half and only your half: another writer is rewriting the other one, and the two will be joined into a single continuous replay."

	compactionSynthesizerRole = "You are joining two independently rewritten halves of one replay into a single continuous piece, and revising a retrospective against the result. Each half was rewritten by someone holding the whole conversation but owning only that half, so both are grounded — and both could be wrong about the other's territory. Your answer is the finished text, not an account of how you produced it."
)

// compactionReplayPrompt is DEFAULT_REPLAY_COMPACTION_PROMPT: the instruction
// when the recent turns are kept.
const compactionReplayPrompt = `The conversation has grown too long and its earlier part is about to be discarded. Write the replay that takes its place.

Your replay will be **prepended directly to the messages that remain** — the most recent turns of this conversation, which are still there and which you will read immediately after this. Write it so that seam is invisible.

Write in the **first person, present continuous tense**, in your own voice, in the same prose as the turns it sits in front of. You are not reporting on a conversation to someone else and you are not recounting something finished: you are picking the conversation back up, and everything in the replay is the situation as it stands right now.

Write **every step** as the step itself, then what came of it. These are the shapes — reuse them:

- "The user is asking me how Rayleigh scattering depends on wavelength."
- "I am explaining that it goes with the inverse fourth power." — then what the user made of it.
- "The user objects that sunsets are red. I am showing why that follows from the same law."
- "We have settled that the answer holds for particles much smaller than the wavelength."

This holds for the middle of the replay and not only its first and last sentences. A step that opens by reporting itself has changed voice, and the seam this replay exists to remove is back.

Written in the past tense the replay reads as history, and history is something you are entitled to doubt: you will re-derive what is already settled and treat a correction that still stands as something that merely once happened. Written in the present it is the state of play, which is what it actually is. Every sentence, not only the first and the last.

Carry all of this:

- **What the user asks, verbatim.** Quote the user's requests and instructions word for word. Everything else here can be rebuilt; what was asked exists nowhere else once these messages are gone.
- **What has been said, in order.** The questions, your answers, the figures and the reasoning behind them.
- **What was corrected**, and by whom. An answer the user rejected, or one you took back, is the most important kind to keep — without it you will simply give it again.
- **What has been concluded**, including anything ruled out and why.
- **Where the conversation is now**, and what is open.

Do not invent anything you are not sure of. A figure or a quotation you cannot see in the conversation is one you do not know.

Write the replay and stop. Do not continue the conversation that follows these instructions, and do not copy any part of it back: it is what you are replacing.`

// compactionFullPrompt is DEFAULT_FULL_COMPACTION_PROMPT: the instruction when
// nothing of the conversation is kept but the latest request.
const compactionFullPrompt = `Everything above is about to be deleted. What you write now replaces it completely: when the conversation resumes, your summary will be the only thing there. Nothing you leave out can be recovered, and nothing you get wrong can be checked against anything.

Write it for whoever answers next — which is you, without any memory of this. Not a report for a person, not a wrap-up, not an answer to the user. A state record, written so the conversation can continue without asking the user anything again.

This request is a system operation, not a new instruction from the user. The goal, the open question and the work in progress are all the ones that were true *before* this message arrived. Do not treat "write a summary" as the task in hand.

Write every section below, in this order, with its heading exactly as given. A section with nothing in it still gets its heading and the single word ` + "`(none)`" + ` — a missing section reads as an omission by accident, and whoever continues cannot tell which it was.

## Goal
What the user wants from this conversation, and any constraint they placed on how. If the goal changed, give the current one and say what it replaced.

## Standing instructions
Everything the user told you that still applies — preferences, prohibitions, required ways of answering. **Quote these verbatim.** A paraphrased instruction is the one loss with no other source.

## Done
What has been answered or settled, exactly enough that none of it is done twice. If something was settled and later reopened, say both, in that order.

## In progress
What was underway when the conversation was cut, and exactly where it stopped. A partial result goes here in full — it exists nowhere else.

## Ruled out
Answers and approaches already rejected, and why each was. This section is what stops the conversation walking back into the same wall.

## Key facts
What was established that is not obvious: figures, names, definitions, and the reasons behind each decision. **Copy lists, figures and names across verbatim and complete.** Do not summarise or sample them.

## Retrospective
Your own account of how the conversation went: what you understood late, where effort did not pay, any assumption that turned out wrong. This is judgement rather than fact — write it as judgement, and say when you are unsure.

## Next
What the user is waiting for, in order, specific enough to act on without re-deriving it.

Two rules over all of it. **Do not invent anything.** If you cannot recall something, leave it out or say it is uncertain. And do not restate at length material the user can simply ask for again: carry what was decided, not every word of how.`

// compactionWriterMarker is DEFAULT_COUNCIL_WRITER_PROMPT: sent only while the
// review is on, since only the review reads the marker.
const compactionWriterMarker = "## Mark the halfway point\n\n" +
	"Exactly once, put a line containing `" + compactionHalfway + "` and nothing else, at the\n" +
	"point where you are about half way through the conversation you are describing.\n\n" +
	"Measure the half by what happened, not by the words: the marker goes where the\n" +
	"first half of the conversation ends and the second half begins. It must sit on a\n" +
	"boundary between steps — after one step and its outcome are complete, never\n" +
	"inside a step and never inside a fenced block.\n\n" +
	"Nothing else about the replay changes. It is one continuous piece of prose that\n" +
	"happens to carry a marker; do not write headings for the halves, do not\n" +
	"summarise each half, and do not refer to the marker in the text."

const compactionHalfway = "<<<HALFWAY>>>"

// compactionCriticPrompt is DEFAULT_COUNCIL_CRITIC_PROMPT, less its citation
// rules. {{half}}, {{other_half}} and {{half_length}} are substituted.
const compactionCriticPrompt = `You wrote the replay below from the conversation above. The replay has been cut in two and you own the **{{half}} half**. Someone else is rewriting the {{other_half}} half from the same conversation, at the same time, and the two halves will be joined back into one continuous replay.

So: **return the {{half}} half only.** Do not return the {{other_half}} half, do not restate it, do not summarise it, do not lead into it or round it off. It is shown to you for one reason only — so you can check your own half against it and see where your half ends. Anything of it you reproduce will appear twice in the joined replay.

**The user's own words stay.** If your half quotes what the user asked for, that quotation is the most load-bearing text in it — it is the only place the request survives at all once the conversation is gone. Keep it word for word. Do not paraphrase it, do not shorten it, and never drop it to make room.

**You are revising a draft, not writing one.** Start from the text below and change what is wrong with it. Do not re-derive your half from the conversation and write it out afresh: a step that is already right is already done, and rewriting it from scratch is how a correct sentence becomes a different, shorter, wronger one.

What to change in your half:

- **Something that happened and is missing.** A question that was asked, an answer that was given, a correction, a conclusion, an approach that was ruled out. Add it.
- **Something the conversation contradicts.** A figure, a name or a claim that does not match. Correct it to what the conversation shows.
- **Something quoted that does not match.** The user's own words, names and numbers have to be character for character what the conversation holds.
- **Something the {{other_half}} half contradicts.** Ground your half against it: the two are one account of one conversation, and a fact stated one way in your half and another way there is a fact to settle from the conversation.
- **Prose that has drifted.** Fix it. If a step is written as a report of itself — past tense, or narrated as something finished — rewrite it as the step and its outcome. First person, present continuous.

**Length.** Your half is {{half_length}} characters. Return something close to that — within about 10% either way. You are correcting and rewriting it, not condensing it: material you drop is material nothing else will carry.

Answer with the {{half}} half of the replay and nothing else. No heading, no preamble, no note about what you changed, no marker line. Just the prose.`

// compactionSynthesizerPrompt is DEFAULT_COUNCIL_SYNTHESIZER_PROMPT.
// {{original_length}} and {{max_length}} are substituted.
const compactionSynthesizerPrompt = `A replay of the conversation above was cut in half, and each half was rewritten against the whole conversation by a different writer. Join them back into one continuous replay.

Both writers had the whole conversation, so both halves are grounded in it — but each owned only its own half, and either could be wrong about the seam between them or about a fact the other half settles differently. Cross-check the two against each other: where they disagree about the same fact, keep the version that quotes the conversation over the version that describes it; where one states something the other contradicts, keep the one that is specific.

What you are producing is one piece of prose, not two halves stacked up. Make the seam invisible: no heading between them, no marker line, no sentence that restarts or recaps. If the two writers both wrote the same step — once at the end of the first half and once at the start of the second — keep it once.

Keep it in the first person and the present continuous tense, every step written as the step and its outcome after it.

**Length.** The replay was {{original_length}} characters before it was rewritten. Aim at that. You may go up to {{max_length}} — about 10% more — and you should use that allowance only where it takes the extra room to keep something that would otherwise be lost. Do not use it to be more thorough for its own sake, and do not come in far under: material dropped here is material nothing else carries.`

// compactionRetrospectivePrompt is DEFAULT_THINKING_COMPACTION_PROMPT.
const compactionRetrospectivePrompt = `You are writing the retrospective that goes at the top of your own context after compaction. It is not a summary — what happened is written up separately, and repeating it here wastes the space this needs.

You are reading your own reasoning from the part of the conversation about to be discarded, together with what each stretch of it produced.

Write the honest assessment. Be terse. Every line must be something you would want to know before the next hour of this conversation.

## What worked
Approaches that produced progress, stated as approaches rather than as events.

## What did not
Approaches that cost time and produced nothing. Name the failure mode: repeating an answer the user had rejected, reasoning from a misread question, rewriting where narrowing would do.

## Where the time went
The stretches that were expensive against what they achieved, including any turn where the reasoning ran long without converging.

## Do differently
What to do instead. Concrete enough to act on, short enough to remember.

Rules:
- No names, figures or quotations. Those are in the summary. This is about method.
- No narration of the sequence of events. Judgement only.
- Leave out any section with nothing worth saying.
- Terse throughout. A dozen short lines is a good retrospective; a page is a failed one.`

// The summary message's parts (buildSummaryMessage).
const (
	compactionRequestsHeading      = "What the user asked, in their own words — every request made so far, quoted word for word:"
	compactionRetrospectiveHeading = "Retrospective on the part of the conversation this summary replaces — your own assessment, carried forward:"
	compactionSummaryHeading       = "Context summary:"
)
