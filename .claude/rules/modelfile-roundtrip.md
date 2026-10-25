---
paths:
  - parser/parser.go
  - parser/swallowed_directive_test.go
  - server/images.go
  - server/images_test.go
  - server/template_swallowed.go
  - server/template_swallowed_test.go
  - server/create.go
  - server/load_log.go
  - docs/features/modelfile-roundtrip.md
---

# Modelfile round trip (`show --modelfile` → `create`)

- `Model.String` is a carried patch from the fork (`up-modelfile-roundtrip`,
  consumed at the `PATCHES.json` sha), with no marker and no Registry row: fix
  it on the fork first, never here (`docs/protocols/CARRIED-PATCHES.md`). The
  swallowed-directive check is the `template-swallowed` hook: marked lines in
  `parser/parser.go`, `server/create.go` and `server/load_log.go`, Registry
  row `template-swallowed` in `docs/protocols/UPSTREAM-SYNC.md`.
- **`Model.String` in `server/images.go` writes `TEMPLATE` only when the model
  carries one** (`m.Template != nil && m.HasGoTemplate`). A model with no
  template layer has `template.DefaultTemplate` in memory. If `show` writes
  that out, a later `create` stores `{{ .Prompt }}` as a real layer, which
  gives the model a Go template it never had. Guard:
  `TestModelStringWritesOnlyACarriedTemplate`.
- **`ParseFile` refuses a quoted `TEMPLATE` or `SYSTEM` value that swallowed
  directives** (`checkSwallowedDirective` in `parser/parser.go`, detector
  `SwallowedDirective`). An opening
  quote with no closing quote reads on to the next `"`. Every `RENDERER`,
  `PARSER` and `PARAMETER` line in between then becomes template text, and
  the model is created without any of them. This was seen on
  `omnimerge-v4-mtp_tb:27b-iq2m-128k`. The error names the line.
- **A create never carries such a layer on.** A model stored before the check
  (or by another ollama) keeps the swallowed lines in its template layer;
  `refuseSwallowedCarry` (`server/template_swallowed.go`, after
  `ApplyModelfileLayers` in `createModel`) refuses a create FROM it, a
  `tweak model` save included, unless the request sets its own `TEMPLATE` /
  `SYSTEM`. `warnSwallowedTemplate` in `server/load_log.go` warns at every load.
- The `swallowedDirective` pattern covers only lines that start with `FROM`,
  `ADAPTER`, `DRAFT`, `TEMPLATE`, `RENDERER`, `PARSER`, `PARAMETER`,
  `REQUIRES` or `CAPABILITY` (upstream v0.35.1, added by the fork's `95ece51a`)
  followed by an argument. It skips `SYSTEM`, `LICENSE` and
  `MESSAGE` on purpose, because a prompt or template can plausibly start a
  line with those words. It checks multi-line values only.
- Guards in `parser/swallowed_directive_test.go`:
  `TestParseFileRefusesSwallowedDirectives`,
  `TestParseFileRefusesASwallowedCapability`,
  `TestParseFileKeepsMultilineTemplates` (`SYSTEM:` and `###` lines still
  parse) and `TestMultilineParameterRoundTrips`; in
  `server/template_swallowed_test.go`:
  `TestACreateRefusesToCarryOnATemplateThatSwallowedDirectives`. Prose is in
  `docs/features/modelfile-roundtrip.md`.
