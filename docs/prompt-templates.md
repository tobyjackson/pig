# Prompt templates

A prompt template is a markdown file you run with `/name`. It is a saved
prompt with blanks that your arguments fill in.

## Where templates live

- `~/.pig/prompts/*.md` – for you, everywhere
- `.pig/prompts/*.md` – for one project (asked to trust first)
- `--prompt-template <path>` on the command line

The file name is the command name: `review.md` becomes `/review`.

## Format

```markdown
---
description: Review a file for bugs and style
argument-hint: <path> [focus]
---
Read $1 and review it. Focus on: ${2:-correctness and clarity}.
List problems first, most serious at the top, then suggest fixes.
```

Both frontmatter fields are optional. Without `description`, the first
non-empty line is used.

## Arguments

Run `/review src/main.go security`. Inside the template:

| Write | You get |
| --- | --- |
| `$1`, `$2` | the first, second argument |
| `$@` or `$ARGUMENTS` | all arguments, space separated |
| `${2:-default}` | argument 2, or `default` if missing |
| `${@:2}` | arguments from the second onward |
| `${@:2:3}` | three arguments starting at the second |

Quote an argument to keep spaces: `/review main.go "error handling"`.

Templates are not recursive: files in sub-folders are ignored.
