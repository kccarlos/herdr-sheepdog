# Voice bench calibration, 2026-10-03 09:13Z

TTS `gemini/gemini-2.5-flash-tts|say` -> phone emulator -> scratch daemon -> Gemini Live `gemini-3.8-live` (input transcription). 78 clips in 1151 s. Word error rate (character error rate for Chinese) after normalising case, punctuation and fillers.

Sample clips with transcripts: `/Users/kc/Documents/sheepdog-voice-bench/calibration-2026-10-03` (transcripts.md).

| Group | Clips | Mean WER | No transcript |
|---|---|---|---|
| all | 78 | 0.105 | 2 |
| lang=en | 39 | 0.052 | 1 |
| lang=zh | 39 | 0.158 | 1 |
| noise=cafe-10dB lang=en | 6 | 0.000 | 0 |
| noise=cafe-10dB lang=zh | 6 | 0.167 | 0 |
| noise=clean lang=en | 27 | 0.075 | 1 |
| noise=clean lang=zh | 27 | 0.154 | 1 |
| noise=pink-5dB lang=en | 6 | 0.000 | 0 |
| noise=pink-5dB lang=zh | 6 | 0.167 | 0 |
| style=casual lang=en | 4 | 0.042 | 0 |
| style=casual lang=zh | 4 | 0.000 | 0 |
| style=hesitant lang=en | 5 | 0.143 | 0 |
| style=hesitant lang=zh | 5 | 0.430 | 0 |
| style=hurried lang=en | 5 | 0.000 | 0 |
| style=hurried lang=zh | 5 | 0.200 | 1 |
| style=neutral lang=en | 20 | 0.050 | 1 |
| style=neutral lang=zh | 20 | 0.100 | 0 |
| style=tired lang=en | 5 | 0.029 | 0 |
| style=tired lang=zh | 5 | 0.200 | 0 |
| voice=Aoede lang=en | 6 | 0.167 | 1 |
| voice=Aoede lang=zh | 6 | 0.167 | 1 |
| voice=Charon lang=en | 12 | 0.042 | 0 |
| voice=Charon lang=zh | 12 | 0.167 | 0 |
| voice=Kore lang=en | 12 | 0.026 | 0 |
| voice=Kore lang=zh | 12 | 0.075 | 0 |
| voice=Puck lang=en | 6 | 0.036 | 0 |
| voice=Puck lang=zh | 6 | 0.375 | 0 |
| voice=say lang=en | 3 | 0.000 | 0 |
| voice=say lang=zh | 3 | 0.000 | 0 |

## Worst clips

| Lang | Voice | Style | Noise | Said | Heard | WER |
|---|---|---|---|---|---|---|
| zh | Puck | hesitant | clean | 嗯…那个 codex 现在在干嘛？ | 把写 Readme 的那个改叫文档。那个 Codex 现在在干嘛？ | 1.25 |
| en | Aoede | neutral | clean | No wait, stop, don't do that. | (nothing) | 1.00 |
| zh | Aoede | hurried | clean | 把写 README 的那个改叫文档。 | (nothing) | 1.00 |
| zh | Puck | tired | clean | 先不要，等一下再说。 | No, wait. Stop. Don't do that. | 1.00 |
| zh | Charon | neutral | cafe-10dB | 先不要，等一下再说。 | No, wait. Stop. Don't do that. | 1.00 |
| zh | Charon | neutral | pink-5dB | 先不要，等一下再说。 | Not yet. Tell me later. | 1.00 |
| zh | Kore | hesitant | clean | 先不…先不要，等一下再说。 | 不，等等。停下。别那样做。 | 0.90 |
| en | Charon | hesitant | clean | Uh, yes, approve it, just this once. | Move it. Just this once. | 0.33 |
| en | Kore | hesitant | clean | No wait, stop, don't do that. | Wait. Stop. Don't do that. | 0.17 |
| en | Charon | casual | clean | No wait, stop, don't do that. | Now, wait. Stop. Don't do that. | 0.17 |
| en | Kore | tired | clean | Tell bench-claude to run the tests and let me know when they pass. | Claude, to run the tests. And let me know when they pass. | 0.14 |
| en | Puck | hesitant | clean | Uh, what's… What's the codex one doing right now? | What's the Codex one doing right now? | 0.12 |

Estimated spend: $6.045 (live $6.006, tts $0.038, live audio 4.9 min in / 8.4 min out).
