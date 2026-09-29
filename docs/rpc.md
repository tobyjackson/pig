# RPC mode

`pig --mode rpc` turns pig into a back end for your own program. You write
commands to its stdin, one JSON object per line. pig writes events and
responses to stdout, one per line. Split on `\n` only.

Every event from `pig docs json` is also sent here.

## Responses

```json
{"type":"response","command":"get_state","success":true,"id":"your-id","data":{...}}
{"type":"response","command":"set_model","success":false,"id":"your-id","error":"unknown model x/y"}
```

`id` is echoed if you sent one.

## Commands

Prompting:

| command | fields | notes |
| --- | --- | --- |
| `prompt` | `message`, `images` (optional list of `{data, mimeType}`), `streamingBehavior` | starts a run; if busy, queues as steering, or as follow-up when `streamingBehavior` is `followUp` |
| `steer` | `message` | queue for after the current tool calls |
| `follow_up` | `message` | queue for after the run ends |
| `abort` | | stop the run |
| `clear_queue` | | returns `steering` and `followUp` lists |

State:

| command | returns |
| --- | --- |
| `get_state` | `model`, `thinkingLevel`, `isStreaming`, `sessionFile`, `sessionId`, `sessionName`, `autoCompactionEnabled`, `messageCount`, `pendingMessageCount` |
| `get_messages` | the messages the model sees |
| `get_session_stats` | counts, tokens, cost, context use |
| `get_last_assistant_text` | `text` |
| `get_commands` | skills, prompt templates, extension commands |

Model:

| command | fields |
| --- | --- |
| `set_model` | `provider`, `modelId` |
| `cycle_model` | |
| `get_available_models` | |
| `set_thinking_level` | `level` |
| `cycle_thinking_level` | |
| `get_available_thinking_levels` | |
| `set_steering_mode`, `set_follow_up_mode` | `mode`: `all` or `one-at-a-time` |

Sessions:

| command | fields | notes |
| --- | --- | --- |
| `new_session` | | |
| `switch_session` | `sessionPath` | path or partial id |
| `fork` | `entryId` | returns `text` of the chosen message |
| `clone` | | |
| `get_fork_messages` | | user messages with `entryId` and `text` |
| `get_entries` | | every entry plus `leafId` |
| `get_tree` | | nested view |
| `navigate_tree` | `entryId` | move the active point |
| `set_session_name` | `name` | |
| `export_html` | `outputPath` | |
| `compact` | `customInstructions` | returns `summary` |
| `set_auto_compaction` | `enabled` | |
| `bash` | `command`, `id` | runs a shell command; output streams as `bash_execution_update` events with `id` and `delta`; the response has `output` and `exitCode` |
| `reload` | | reload skills, prompts, extensions |

## Example session

```
→ {"type":"get_state","id":"1"}
← {"type":"response","command":"get_state","success":true,"id":"1","data":{...}}
→ {"type":"prompt","message":"list files","id":"2"}
← {"type":"response","command":"prompt","success":true,"id":"2"}
← {"type":"agent_start"}
← {"type":"message_update",...}
← ...
← {"type":"agent_end","messages":[...]}
```

Wait for `agent_end` before reading the final text with
`get_last_assistant_text`.
