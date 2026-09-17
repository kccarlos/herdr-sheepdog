// Package llm implements the cheap-LLM adapter: an OpenAI-compatible
// chat client (base URL, model, and key from config) used text-in/text-out
// only, with no function calling required. It will hold the three prompt
// templates (summarize_block, resolve_instruction, spoken_answer), each
// returning a small JSON object, with input truncation, a timeout, and one
// retry.
package llm
