# Sessions

Every conversation is saved as it happens, so you can close pig and come
back later.

## Files

Sessions live in `~/.pig/sessions/`, in one folder per working directory.
The folder name is the directory path with `/` turned into `-`. Each
session is a `.jsonl` file: one JSON object per line.

Change the location with `sessionDir` in settings or `PIG_SESSION_DIR`.
Use `--no-session` to keep a conversation in memory only.

## Resuming

- `pig -c` continues the newest session for this folder.
- `pig -r` lets you pick one.
- `pig --session <path or id>` opens a specific file. The id can be the
  first few characters of the session UUID.
- `/resume` inside pig does the same.
- `pig sessions` lists them.

## The tree

The file is a tree, not a list. Each entry has an `id` and a `parentId`.
When you go back to an earlier point (`/tree`, or Esc twice) and continue,
new entries hang off that earlier entry. Nothing is deleted; the old path
is still in the file.

`/fork` copies the conversation up to a chosen message into a brand-new
session file and puts that message in the editor for you to change.
`/clone` copies the whole current path into a new file.

## Entry types

The first line is the header:

```json
{"type":"session","version":1,"id":"<uuid>","timestamp":"...","cwd":"/path"}
```

Then entries. All have `type`, `id`, `parentId` (null for the first), and
`timestamp`.

| type | extra fields |
| --- | --- |
| `message` | `message` – a user, assistant, or toolResult message |
| `model_change` | `provider`, `modelId` |
| `thinking_level_change` | `thinkingLevel` |
| `compaction` | `summary`, `firstKeptEntryId`, `tokensBefore`, `usage` |
| `session_info` | `name` |
| `custom` | `customType`, `data` – extension state, never sent to the model |

## Compaction

Models have a limit on how much they can read at once. When the
conversation gets close, pig asks the model to summarise the older part
and keeps the recent part word for word. The summary goes in a
`compaction` entry. The full history stays in the file.

- Automatic by default; settings `compaction.reserveTokens` (default 16384)
  and `compaction.keepRecentTokens` (default 20000) tune when and how much.
- `/compact` does it now. `/compact focus on the database changes` adds a
  hint for the summary.
- If a request fails because the context is too long, pig compacts and
  retries once.

## Export

`/export notes.html` or `pig --export session.jsonl out.html` writes a
readable HTML page.
