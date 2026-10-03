---
paths:
  - parser/parser.go
  - parser/swallowed_directive_test.go
  - server/images.go
  - server/images_test.go
  - docs/features/modelfile-roundtrip.md
---

# Modelfile round trip (`show --modelfile` → `create`)

- This is a carried patch from the fork (`up-modelfile-roundtrip`, consumed at
  the `PATCHES.json` sha). There is no `// xollama-hook:` marker and no
  Registry row. Fix it on the fork first, never here. See
  `docs/protocols/CARRIED-PATCHES.md`.
- **`Model.String` in `server/images.go` writes `TEMPLATE` only when the model
  carries one** (`m.Template != nil && m.HasGoTemplate`). A model with no
  template layer has `template.DefaultTemplate` in memory. If `show` writes
  that out, a later `create` stores `{{ .Prompt }}` as a real layer, which
  gives the model a Go template it never had. Guard:
  `TestModelStringWritesOnlyACarriedTemplate`.
- **`ParseFile` refuses a quoted `TEMPLATE` or `SYSTEM` value that swallowed
  directives** (`checkSwallowedDirective` in `parser/parser.go`). An opening
  quote with no closing quote reads on to the next `"`. Every `RENDERER`,
  `PARSER` and `PARAMETER` line in between then becomes template text, and
  the model is created without any of them. This was seen on
  `omnimerge-v4-mtp_tb:27b-iq2m-128k`. The error names the line.
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
  parse) and `TestMultilineParameterRoundTrips`. Prose is in
  `docs/features/modelfile-roundtrip.md`.
