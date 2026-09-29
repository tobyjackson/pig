# Extensions

An extension is a program that pig runs alongside itself. It can add tools
the model may call, add `/commands` for you, and step in when tools run.
Write it in any language that can read and write lines of text.

## Where extensions live

- `~/.pig/extensions/` – any executable file here is loaded
- `.pig/extensions/` – per project (asked to trust first)
- `-e <path>` on the command line, for trying one out

Make the file executable (`chmod +x`) and give it a shebang line such as
`#!/usr/bin/env python3`.

## How pig and the extension talk

Lines of JSON. pig writes to the extension's stdin; the extension writes to
its stdout. Every line is one JSON object with a `type`. Requests from pig
carry an `id`; the reply must echo the same `id`.

Write logs to stderr, never stdout.

### 1. Handshake

pig sends:

```json
{"type":"init","cwd":"/path/to/project","protocol":1}
```

The extension answers once:

```json
{"type":"ready","name":"hello",
 "tools":[{"name":"shout","description":"Upper-case text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}],
 "commands":[{"name":"hello","description":"Say hello"}],
 "events":["tool_call","tool_result","before_agent_start","agent_end","session_start"]}
```

Leave out anything you do not need. `parameters` is a JSON schema.

### 2. Tool calls

When the model calls one of your tools:

```json
{"type":"tool_call","id":"7","name":"shout","toolCallId":"toolu_1","input":{"text":"hi"}}
```

Reply:

```json
{"type":"tool_result","id":"7","content":[{"type":"text","text":"HI"}],"isError":false}
```

`content` may also hold `{"type":"image","data":"<base64>","mimeType":"image/png"}`.
A shortcut: `{"type":"tool_result","id":"7","text":"HI"}`.

An extension tool with the same name as a built-in (for example `bash`)
replaces it.

### 3. Commands

When the user types `/hello some words`:

```json
{"type":"command","id":"8","name":"hello","args":"some words"}
```

Reply with either or both of:

```json
{"type":"response","id":"8","data":{"message":"text sent to the model as the user","notify":"text shown to the user"}}
```

### 4. Events

For each event you listed in `ready`, pig sends:

```json
{"type":"event","id":"9","event":"tool_call","data":{...}}
```

and waits (up to 30 seconds) for:

```json
{"type":"response","id":"9","data":{...}}
```

Reply with `"data":{}` when you have nothing to change.

| event | data you receive | what you may return in data |
| --- | --- | --- |
| `session_start` | `reason`, `sessionFile`, `cwd` | nothing |
| `before_agent_start` | `prompt`, `systemPrompt` | `systemPrompt` – a replacement system prompt for this run |
| `tool_call` | `toolName`, `toolCallId`, `input` | `block: true` with `reason`, or `input` – changed arguments |
| `tool_result` | `toolName`, `toolCallId`, `input`, `content`, `isError` | `content`, `isError` – a changed result |
| `agent_end` | nothing | nothing |

Several extensions may handle the same event; each sees the previous one's
changes.

### 5. Messages to the user

Any time, the extension may write:

```json
{"type":"notify","message":"something happened","level":"info"}
```

`level` is `info`, `warning`, or `error`.

### 6. Shutdown

pig sends `{"type":"shutdown"}` and closes stdin. Exit when you see either.

## Example

`examples/extensions/hello.py` in the repo does all of the above in about
fifty lines of Python. Copy it to `~/.pig/extensions/hello.py`, make it
executable, start pig, and try `/hello` or ask the model to "shout hello".

## Safety

Extensions run with your permissions. Only install ones you trust. Project
extensions in `.pig/extensions` are not loaded until you trust the folder.
