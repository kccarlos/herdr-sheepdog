// Package llm implements the cheap-LLM adapter (plan §3.4): a Client for
// any OpenAI-compatible chat endpoint used strictly text-in/text-out
// (decision D7 — no function calling), plus the three prompt templates the
// Supervisor needs: SummarizeBlock, ResolveInstruction and SpokenAnswer.
//
// The Client takes a base URL, a model and a key pool (the Gemini pool when
// the base URL is Gemini's OpenAI-compatible endpoint, or any non-empty
// string for a local Ollama). Every call has a 15 s timeout, one retry on
// transient failures, and rate-limit handling that honours Retry-After /
// retryDelay and rotates to the next key when there is one. It asks for a
// JSON object response and, because not every backend honours that,
// extracts the first JSON object from whatever text comes back.
//
// Each template function renders its prompts/*.txt file, truncates screen
// input to the last 8 KB (whole lines, newest kept), validates the JSON
// reply and enforces decision D9 in Go: spoken text is cut to two
// sentences and scrubbed of paths, hashes and URLs regardless of what the
// model produced.
package llm
