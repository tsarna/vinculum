# LLM Clients (`client "openai"`)

Vinculum can call LLM APIs synchronously using `client "openai"` blocks and the
`call()` function. The `openai` client type works with OpenAI's API and any
OpenAI-compatible provider (Groq, Together AI, Mistral, Ollama, LM Studio, Google
Gemini, and others).

---

## `client "openai" "<name>"`

Connects to OpenAI's API or any OpenAI-compatible endpoint.

```hcl
client "openai" "name" {
    # Required
    api_key = env.OPENAI_API_KEY

    # Required: default model used when the call() request doesn't specify one
    model = "gpt-4o"

    # Optional: override base URL for OpenAI-compatible providers
    # Default: "https://api.openai.com/v1"
    base_url = "https://api.groq.com/openai/v1"

    # Optional: default max tokens for responses
    # If unset, the provider default applies
    max_tokens = 4096

    # Optional: default sampling temperature (0.0–2.0; lower = more deterministic)
    # If unset, the provider default applies
    temperature = 0.7

    # Optional: HTTP request timeout
    # Default: "120s"
    timeout = "60s"

    # Optional: maximum total character length of user/assistant message content.
    # System messages are not counted (they are developer-controlled).
    # When exceeded, call() returns stop_reason="error" with error.code="input_too_long"
    # without making an API call.
    max_input_length = 8000

    disabled = false  # optional
}
```

The client is available in expressions as `client.<name>` and can be passed to
`call()`.

### Common OpenAI-compatible providers

| Provider | `base_url` | Notes |
|---|---|---|
| OpenAI | *(default)* | GPT-4o, GPT-4o-mini, etc. |
| Groq | `"https://api.groq.com/openai/v1"` | Fast inference; Llama, Mixtral |
| Together AI | `"https://api.together.xyz/v1"` | Many open models |
| Mistral | `"https://api.mistral.ai/v1"` | Mistral models |
| Google Gemini | `"https://generativelanguage.googleapis.com/v1beta/openai/"` | Use Gemini API key |
| Ollama | `"http://localhost:11434/v1"` | Local models; set `api_key = "ollama"` |
| LM Studio | `"http://localhost:1234/v1"` | Local models; any non-empty `api_key` |

---

## `call(ctx, client, request)`

Makes a synchronous LLM request and returns a response object. Blocks until the
API responds or the timeout is reached.

```
call(ctx, client, request) -> response
```

- **`ctx`** — the current execution context (same `ctx` available in all action expressions)
- **`client`** — a client capsule value (e.g. `client.myassistant`)
- **`request`** — an object describing the LLM request (see below)
- **returns** — a response object (see below); always returns an object, never null

`call()` is valid in any expression context: HTTP handler actions, subscription
actions, cron actions, etc.

**Note on latency:** LLM API calls typically take 1–30+ seconds. In HTTP handlers
this blocks the handler goroutine for the duration of the call. In subscription
actions, consider using `queue_size` on the subscription to avoid stalling upstream
message producers.

### Request Object

```hcl
{
    # Required: conversation messages
    messages = [
        { role = "user", content = "What is 2+2?" },
    ]

    # Optional: system prompt shorthand
    # Equivalent to prepending { role = "system", content = "..." } to messages
    system = "Answer concisely."

    # Optional: override the client's default model for this call
    model = "gpt-4o-mini"

    # Optional: override the client's default max_tokens for this call
    max_tokens = 512

    # Optional: override the client's default temperature for this call
    temperature = 0.0
}
```

**Message roles:**

| Role | Meaning |
|---|---|
| `"system"` | Instructions/persona for the LLM (typically first message, or use `system` shorthand) |
| `"user"` | Input from the human or application |
| `"assistant"` | Previous LLM response (used when replaying conversation history) |

### Response Object

`call()` always returns a response object. On failure the object has
`stop_reason = "error"` and a populated `error` field rather than returning null
— this makes it safe to access `result.content` without nil-checking when you
don't need error handling.

```hcl
{
    # The assistant's response text; null on error
    content = "The answer is 4."

    # Why the LLM stopped:
    # "stop"       — natural end of response
    # "max_tokens" — hit the token limit
    # "error"      — call failed (check error field)
    stop_reason = "stop"

    # Model that actually served the request; null on error
    model = "gpt-4o-2024-08-06"

    # Token usage; null on error
    usage = {
        input_tokens  = 10
        output_tokens = 5
        total_tokens  = 15
    }

    # Error details; null on success
    error = null
}
```

Error response shape:

```hcl
{
    content     = null
    stop_reason = "error"
    model       = null
    usage       = null
    error = {
        code    = "401"
        message = "Invalid API key"
    }
}
```

---

## Security: Prompt Injection

Prompt injection is when user-controlled input manipulates the LLM into ignoring
its instructions or performing unintended actions. The mitigations below reduce risk.

### Structural separation — use `system`

Always put your instructions in the `system` field. All modern models are trained
to give higher trust to system messages than user messages.

```hcl
# Good: instructions structurally separated from user input
call(ctx, client.gpt, {
    system   = "Summarize the following text."
    messages = [{ role = "user", content = user_input }]
})

# Bad: instructions and user input concatenated
call(ctx, client.gpt, {
    messages = [{ role = "user", content = "Summarize this: ${user_input}" }]
})
```

### `llm_wrap(content)` — delimiter wrapping

Wraps a string in `<user_input>` XML-like delimiters to signal to the model where
untrusted input begins and ends. The system prompt should reference these tags:

```
<user_input>
{content}
</user_input>
```

```hcl
call(ctx, client.gpt, {
    system   = "Summarize the text in the <user_input> tags in 2-3 sentences."
    messages = [{ role = "user", content = llm_wrap(getbody()) }]
})
```

### `max_input_length` — cap untrusted input size

Long inputs increase injection surface area and incur unbounded token costs. Set
`max_input_length` on the client to reject oversized inputs before any API call:

```hcl
client "openai" "gpt" {
    api_key          = env.OPENAI_API_KEY
    model            = "gpt-4o-mini"
    max_input_length = 8000   # characters of user/assistant content
}
```

When the limit is exceeded, `call()` returns `stop_reason = "error"` with
`error.code = "input_too_long"` — no API call is made. System messages are not
counted toward the limit.

---

## Examples

### Simple one-shot completion

```hcl
client "openai" "gpt" {
    api_key          = env.OPENAI_API_KEY
    model            = "gpt-4o-mini"
    max_input_length = 8000
}

server "http" "api" {
    listen = ":8080"

    handle "POST /ask" {
        action = respond(200, call(ctx, client.gpt, {
            system   = "Answer the question in the <user_input> tags concisely."
            messages = [{ role = "user", content = llm_wrap(getbody()) }]
        }).content)
    }
}
```

### Using Groq (OpenAI-compatible)

```hcl
client "openai" "groq" {
    api_key  = env.GROQ_API_KEY
    base_url = "https://api.groq.com/openai/v1"
    model    = "llama-3.3-70b-versatile"
}
```

### Local model via Ollama

```hcl
client "openai" "local" {
    api_key  = "ollama"   # Ollama ignores the key but requires a non-empty value
    base_url = "http://localhost:11434/v1"
    model    = "llama3.2"
    timeout  = "300s"     # local inference can be slow
}
```

### Classify messages and route to bus topics

```hcl
client "openai" "classifier" {
    api_key          = env.OPENAI_API_KEY
    model            = "gpt-4o-mini"
    max_tokens       = 10
    temperature      = 0.0
    max_input_length = 2000
}

subscription "classify" {
    target = bus.inbound
    topics = ["raw/#"]
    action = send(ctx, bus.classified, "msg/${call(ctx, client.classifier, {
        system   = "Classify the text in the <user_input> tags as one word: urgent, normal, or spam."
        messages = [{ role = "user", content = llm_wrap(ctx.msg) }]
    }).content}", ctx.msg)
}
```

### Multi-turn conversation with `var` history

Note that vars are global, so this doesn't allow for simultaneous conversations.

```hcl
client "openai" "assistant" {
    api_key          = env.OPENAI_API_KEY
    model            = "gpt-4o"
    max_input_length = 8000
}

var "history" {
    value = []
}

server "http" "chat" {
    listen = ":8080"

    handle "POST /chat" {
        action = {
            user_msg = { role = "user", content = llm_wrap(jsondecode(getbody()).message) }
            result   = call(ctx, client.assistant, {
                system   = "You are a helpful assistant. The user's messages are wrapped in <user_input> tags."
                messages = concat(get(var.history), [user_msg])
            })
            _        = set(var.history, concat(
                get(var.history),
                [user_msg, { role = "assistant", content = result.content }]
            ))
            _        = respond(200, jsonencode({ reply = result.content }))
        }
    }
}
```
