# Check prompt cache behavior with Ollama

Use the local diagnostic proxy to inspect the requests Workshop sends to
Ollama without saving prompt text or credentials:

```bash
python3 scripts/ollama_cache_proxy.py --output /tmp/workshop-cache.jsonl
```

In another terminal, run Workshop with the proxy as the Ollama provider's
temporary base URL:

```bash
WORKSHOP_PROVIDER_OLLAMA_BASE_URL=http://127.0.0.1:11436/v1 \
  go run ./cmd/workshop --provider ollama
```

Send a simple prompt, a follow-up in the same thread, and a prompt that uses a
tool. The proxy prints one JSON summary per model request and appends it to the
output file. It buffers each upstream response, so use it only for diagnostics.
The log contains hashes, counts, and timings; it contains no prompt text,
tool schema, or API key.

## Read the results

- `matching_initial_messages` counts whole messages identical to the previous
  request's prefix. A tool follow-up should preserve every earlier message and
  append the assistant tool call and tool result.
- `system_hash` and `tools_hash` should remain stable while the system prompt
  and tool set are unchanged. A changed hash identifies a possible cache
  breakpoint before the new user content.
- `prompt_tokens` is the total input size. `cached_tokens` is an actual hit
  count **only when the upstream reports it**. `null` means unavailable, not
  zero. Compare `upstream_first_byte_ms` for controlled identical-prefix runs
  when the count is unavailable; timing alone cannot establish an exact hit
  rate.

On OpenAI, check the provider's reported cached-token count after switching:
[Chat Completions](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
uses `usage.prompt_tokens_details.cached_tokens`; Responses uses
`usage.input_tokens_details.cached_tokens`. OpenAI's [prompt caching
guide](https://developers.openai.com/api/docs/guides/prompt-caching) explains
the model-specific minimum cacheable length and cache breakpoints. Ollama's
local cache uses a different engine, so its hit rate cannot predict OpenAI's
hit rate exactly.

## Local validation on 9 November 2026

This run used Ollama 0.20.0, `qwen3:8b` on GPU, and a 4,096-token context.
The controlled probe sent identical system text and one tool definition with
at most eight output tokens. The [native Ollama usage
fields](https://docs.ollama.com/api/usage) define
`prompt_eval_cached_count`, but this installed server omitted it, including on
the native `/api/chat` endpoint. The OpenAI-compatible endpoint also omitted
`usage.prompt_tokens_details.cached_tokens`. Workshop displayed `↻ 0` because
the current adapter turns an absent count into zero; that display does **not**
prove caching was disabled.

| Controlled request | Prompt tokens | Native prompt evaluation | Elapsed time |
| --- | ---: | ---: | ---: |
| Cold, unique prefix | 852 | 472 ms | 668 ms |
| Same request, first repeat | 852 | 16 ms | 202 ms |
| Same request, second repeat | 852 | 16 ms | 211 ms |
| Changed only the final user message | 852 | 36 ms | 220 ms |
| Changed the start of the system message | 849 | 459 ms | 646 ms |

These timings show local prefix reuse for short requests. A separate prompt
that filled the 4,096-token context took about 2.3 seconds of prompt
evaluation on every identical repeat. Validate within the model's context
window; this setup does not test longer OpenAI contexts.

The proxy captured real Workshop requests before and after a temporary
Workshop prompt-view fix. The system message and all 11 tool definitions had
identical hashes across threads. The first new-thread request retained six
earlier static messages despite a different `prompt_cache_key`; its 178 ms
first-byte time was consistent with a warm local prefix.

Workshop sets `prompt_cache_key` to the thread ID, so separate threads use
different keys. OpenAI's [routing guidance](https://developers.openai.com/api/docs/guides/prompt-caching)
says the key is optional for GPT-5.6 and later and helps route related
requests on earlier models. The Ollama run does not validate cross-thread
OpenAI routing; include separate threads in the OpenAI canary.

| Tool round trip | Follow-up messages | Prompt tokens | Upstream first byte |
| --- | ---: | ---: | ---: |
| Before fix: system prompt and five guardrails repeated | 15 | 3,630 | 1,643 ms |
| After fix: earlier messages preserved, tool call/result appended | 9 | 2,561 | 213 ms |

The request shape proves that the duplicate virtual prompts were removed.
Latency supports better local cache reuse, though model output varied between
these two runs. Ordinary user follow-ups already kept their prior messages as
an identical prefix.

[Ore v1.4.2](https://github.com/andrewhowdencom/ore/tree/v1.4.2) now keeps
inference-only prompt views inside its pipeline, replacing the temporary
Workshop fix. A fresh Workshop tool round trip with that release and no
Workshop cleanup transform sent 7 messages initially and 9 on the tool
follow-up. All 7 earlier messages matched exactly; the system message and 11
tool definitions retained their hashes. Prompt tokens were 2,480 then 2,706,
and the follow-up's upstream first byte arrived in 275 ms. Ollama still omitted
`cached_tokens`, so this verifies request shape rather than an exact hit count.

Before returning to OpenAI, run a small canary on the actual target model and
collect its reported cached-token counts across a normal turn, a tool round
trip, and a longer conversation. Ollama does not establish OpenAI routing,
retention, tokenization, or billing behavior.
