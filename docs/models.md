# Models

pig knows Anthropic's Claude models, a few OpenAI models, and OpenRouter
out of the box. Add anything else that speaks the Anthropic Messages API
or the OpenAI Chat Completions API with `~/.pig/models.json`.

## Keys

In order, pig looks for a key in: the provider's `apiKey` in models.json,
`~/.pig/auth.json` (written by `pig login <provider>`), then the
environment (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`).

## OpenRouter

[OpenRouter](https://openrouter.ai) gives one key access to models from
many vendors. Get a key at openrouter.ai/keys, then:

```sh
pig login openrouter            # or: export OPENROUTER_API_KEY=sk-or-...
pig models claude               # search the live catalogue
pig --model openrouter/anthropic/claude-fable-5.1
```

Any `openrouter/<vendor>/<model>` id works, even ones pig has never seen;
`pig models [search]` fetches the current list with context sizes and
prices. Cost in the status line is exact: OpenRouter reports what each
request cost. Inside pig, `/model openrouter/deepseek/deepseek-v4.1-flash`
switches directly.

`apiKey` in models.json may be a plain string, `$ENV_VAR`, or `!command`
(the command's output is the key).

## models.json

```json
{
  "providers": {
    "ollama": {
      "baseUrl": "http://localhost:11434/v1",
      "api": "openai-completions",
      "apiKey": "ollama",
      "models": [
        { "id": "qwen2.5-coder:7b" },
        { "id": "llama3.1:8b", "name": "Llama 3.1 8B", "contextWindow": 128000, "maxTokens": 16384 }
      ]
    },
    "openrouter": {
      "baseUrl": "https://openrouter.ai/api/v1",
      "api": "openai-completions",
      "apiKey": "$OPENROUTER_API_KEY",
      "headers": { "HTTP-Referer": "https://example.com" },
      "models": [
        { "id": "anthropic/claude-sonnet-5", "reasoning": true, "input": ["text", "image"],
          "cost": { "input": 3, "output": 15, "cacheRead": 0.3, "cacheWrite": 3.75 } }
      ]
    }
  }
}
```

Provider fields: `baseUrl`, `api` (`anthropic-messages` or
`openai-completions`), `apiKey`, `headers`, `models`.

Model fields:

| field | default | meaning |
| --- | --- | --- |
| `id` | required | sent to the API |
| `name` | id | shown in lists |
| `api` | provider's | override per model |
| `reasoning` | false | supports a thinking level |
| `input` | `["text"]` | add `"image"` for vision |
| `contextWindow` | 128000 | tokens the model can read |
| `maxTokens` | 16384 | longest reply |
| `cost` | zeros | dollars per million tokens: `input`, `output`, `cacheRead`, `cacheWrite` |

A model with the same provider and id as a built-in replaces it.

## Choosing a model

- `pig --model sonnet` matches by id or name, case-insensitive.
- `pig --model openai/gpt-5` is exact.
- `/model` inside pig opens a picker; `Ctrl+S` there saves the default.
- `Ctrl+P` cycles through models that have a key.

## Thinking levels

`off minimal low medium high xhigh max`. On current Claude models these map
to adaptive thinking with an effort setting. On older Claude models they
set a thinking budget. On OpenAI models they set `reasoning_effort`.
Models without `reasoning` ignore the level.
