# AGENTS.md

pig is a small coding agent for the terminal, in Go. It gives a model four
tools (read, write, edit, bash) and a short system prompt; everything else is
added by the user through skills, prompt templates, and extensions. It is a
port of [pi](https://github.com/earendil-works/pi) and does not track pi's
releases.

## Commands

```sh
go build -o pig ./cmd/pig      # build
go test ./...                  # tests (all of them; there is no fast subset)
go vet ./...                   # vet
gofmt -l .                     # must print nothing
go run ./internal/fakeserver & # fake model, for trying pig offline
```

There is no Makefile, no linter config, and no CI other than the release
workflow. Run tests before you claim a change works; several packages have no
test files, so a passing run is not proof of broad coverage.

## Small is the point

pig is deliberately one module of about 9k lines with four tools. pi has eight
tools and twelve packages. Do not add a fifth tool, a new package, or a
dependency without a clear reason, and prefer editing an existing file to
creating one. The `go.mod` require list is intentionally tiny: the standard
library plus three Charmbracelet modules for the TUI.

## Ported from pi

Seven files are copied, not merely inspired, from pi's TypeScript, and carry a
header saying so:

```
tools/read.go  tools/bash.go  tools/edit.go  tools/write.go
runtime/systemprompt.go  resources/skills.go  resources/context.go
```

Their tool descriptions, JSON schemas, prompt snippets, the system prompt's
wording and section order, and the context-file names and precedence must stay
faithful to pi. If you change them, say in the commit why. `LICENSE` carries
pi's copyright; keep that notice intact.

## Conventions

- Error messages are lowercase, no trailing punctuation, and say what to do
  next: `no model matches %q (try --list-models)`. The exception is
  model-facing tool errors, which are ported and capitalised.
- Packages, exported types, and the non-obvious exported functions have a
  doc comment saying why they exist, not just what they do. Small interface
  methods on the tools (`Name`, `Parameters`, `Execute`) are left bare.
- Prose in code, docs, and commits is wrapped at about 76 columns and uses
  British spelling where the existing text does.
- The system prompt is built in `runtime/systemprompt.go`. Guidelines there
  are deduplicated and ordered, and are part of the model's behaviour, not
  decoration. Changing a guideline changes results.
- User-facing docs live in `docs/*.md` and are embedded by `docs/docs.go`, so
  they must be added to `docs/index.md` and the README's topic list to be
  reachable.

## Commits

Subject line is a short imperative phrase; the body is prose, wrapped, and
explains what was wrong and why the fix is right. A commit that fixes a defect
should name the defect and the case that triggered it. Do not pad a commit
body with a file-by-file list; `git show --stat` already has that.

## README accuracy

The README's claims about pi have been wrong before: it described pi as it was
when the port started. Anything you write about pi's size, tools, or packages
should be checked against the upstream repo, and the README should describe
pig's own design rather than what pi happens to do today.

## Releasing

Tag and push; the workflow tests, cross-compiles five binaries, and opens a
draft release.

```sh
git tag v0.1.0 && git push origin v0.1.0
```

The version a binary reports is injected at build time with
`-X main.version=${tag}`, so it is empty in a plain `go build`. Do not publish
a release: leave the draft for a human.
