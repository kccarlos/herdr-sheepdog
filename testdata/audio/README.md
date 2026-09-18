# Test audio

Both files are committed with `git add -f` because `*.wav` is gitignored.

- `question.wav` — "What is going on with the agents?", 16 kHz PCM16 mono,
  1.9 s. Input for `sheepdog voice-test --in`. Regenerate with:

  ```sh
  say -v Samantha -o question.aiff "What is going on with the agents?"
  afconvert -f WAVE -d LEI16@16000 -c 1 question.aiff question.wav
  ```

- `reply-briefing.wav` — the model's reply from the WP2 acceptance run
  (`gemini-3.8-live`, echo tools), 24 kHz PCM16 mono, 3.7 s: "Echo test: the
  briefing tool was called with no arguments." Useful as a playback sample
  for the `/voice` page (WP6). Regenerate with
  `sheepdog voice-test --in testdata/audio/question.wav --out reply.wav`.
