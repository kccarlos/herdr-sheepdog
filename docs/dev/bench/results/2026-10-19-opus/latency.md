## opus-medium/merged-raw (18 runs)

| Turn kind | n (first / rest) | p50 first | p50 rest | p90 rest | p50 all | p90 all |
|---|---|---|---|---|---|---|
| announce | 18 / 92 | 6.7 s | 4.7 s | 7.0 s | 4.8 s | 7.2 s |
| investigation | 4 / 1 | 5.3 s | 5.6 s | 5.6 s | 5.6 s | 6.9 s |
| utterance | 18 / 208 | 5.1 s | 7.5 s | 12.3 s | 7.4 s | 12.5 s |

- announce turns over 6 s (answered, not fallen back): 31/110 (28 %)
- utterance turns over 25 s (answered): 1/226
- fallbacks by (kind, reason): {('announce', 'yielded'): 3, ('announce', 'timeout'): 1, ('announce', 'queue_timeout'): 3}
- daemon sentence spoken for a timed-out announce (push_decision via=template reason=timeout): 4
- prompt-cache hit rate: 61.0 % (5828737 of 9562184 prompt tokens)
- failed model calls: 1 (refusals 0); thinking blocks dropped events: 0; rate limited: 0
- orchestrator spend estimate: $22.53 ($1.25 per run)

## deepseek/merged-raw (18 runs)

| Turn kind | n (first / rest) | p50 first | p50 rest | p90 rest | p50 all | p90 all |
|---|---|---|---|---|---|---|
| announce | 18 / 100 | 1.7 s | 1.6 s | 2.0 s | 1.6 s | 2.0 s |
| investigation | 3 / 1 | 1.6 s | 4.0 s | 4.0 s | 1.7 s | 3.4 s |
| utterance | 18 / 209 | 2.9 s | 3.0 s | 5.3 s | 3.0 s | 5.6 s |

- announce turns over 6 s (answered, not fallen back): 0/118 (0 %)
- utterance turns over 25 s (answered): 0/227
- fallbacks by (kind, reason): {}
- daemon sentence spoken for a timed-out announce (push_decision via=template reason=timeout): 0
- prompt-cache hit rate: 81.9 % (5741696 of 7010037 prompt tokens)
- failed model calls: 0 (refusals 0); thinking blocks dropped events: 0; rate limited: 0
- orchestrator spend estimate: $2.18 ($0.12 per run)

