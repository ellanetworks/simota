# Ella SIM OTA - AI Coding Agent Instructions

## Code Comments

**Write zero comments.** Do not add explanatory comments, section headers, TODOs, doc comments, field comments, or inline comments to any Go or UI code you write or modify, not even for non-obvious logic.

The only allowed comments are:

- **Spec citations**, written bare, one per line, directly above a function, method, type, or const/var declaration. No prose, no trailing period:

  ```go
  // ETSI TS 102 225 clause 5.1.2
  // TS 31.115 clause 4.1
  // RFC 4493 section 2.4
  ```

  3GPP, ETSI and ISO/IEC specs use `clause`; RFCs use `section`. ETSI deliverables always carry the `ETSI` prefix (`ETSI TS 102 226`); 3GPP ones never do (`TS 31.115`). Never put a citation inside a function body or on a struct field. Only cite a clause that you have confirmed in the spec (specs are in `../3gpp`). Never guess a clause number.
- **Comments that tooling requires**: `//go:build`, `//go:embed`, `//go:generate`, `//nolint:<linter>` (with no trailing reason), `// eslint-disable…`, `// oxlint-disable…`, `// @ts-expect-error`.

**Do not touch existing citations** unless your change makes one wrong. In that case, fix it or delete it.
