import Foundation

/// Captured from `sheepdog serve --brain=fake --herdr=mock --voice=off`
/// (daemon 7a192e9) with a blocked Codex prompt injected from
/// `testdata/herdr/codex-approval-prompt.screen.txt`. Kept as literals so
/// the tests need no resource wiring. The fraction digits of `ts` are the
/// daemon's own (Go trims trailing zeros). The page is trimmed from a
/// longer one, one item is out of order on purpose, and its last push is
/// added by hand to cover `urgency: interrupt`.
enum Fixtures {
    static let conversationPage = #"""
    {
      "items": [
        {"id": 1790065877490932, "ts": "2026-09-22T08:31:17.490932Z", "kind": "notice", "text": "Sheepdog started."},
        {"id": 1790066540916805, "ts": "2026-09-22T08:42:20.916805Z", "kind": "shepherd", "text": "what's going on", "via": "typed"},
        {"id": 1790066540919059, "ts": "2026-09-22T08:42:20.919059Z", "kind": "dog", "text": "One agent: auth is asking would you like to run the following command."},
        {"id": 1790065929374092, "ts": "2026-09-22T08:32:09.374092Z", "kind": "approval", "text": "docs is asking do you want to create claude.txt.", "pending_id": "ap-tlrbpl-2", "answered": false, "resolution": "expired"},
        {"id": 1790127611283964, "ts": "2026-09-23T01:40:11.283964Z", "kind": "notice", "text": "Sheepdog started."},
        {"id": 1790127618456029, "ts": "2026-09-23T01:40:18.456029Z", "kind": "shepherd", "text": "what is everyone doing?", "via": "typed"},
        {"id": 1790127618456893, "ts": "2026-09-23T01:40:18.456893Z", "kind": "dog", "text": "I could not do that: herdr: pane not found:."},
        {
          "id": 1790127629425157, "ts": "2026-09-23T01:40:29.425157Z", "kind": "approval",
          "text": "billing asks to run printf '%s\\n' 'hello from codex.' > hello.txt.",
          "via": "tap", "agent": "billing", "pending_id": "ap-tlsnbh-1",
          "options": [
            {"index": 1, "label": "Yes, proceed", "keys": ["y"], "default": true},
            {"index": 2, "label": "Yes, and don't ask again for commands that start with `printf '%s\\n' 'hello from codex.' > hello.txt`", "keys": ["p"], "default": false},
            {"index": 3, "label": "No, and tell Codex what to do differently", "keys": ["esc"], "default": false}
          ],
          "answered": true, "answer": "Yes, proceed", "resolution": "confirmed"
        },
        {"id": 1790127629426365, "ts": "2026-09-23T01:40:29.426365Z", "kind": "push", "text": "billing asks to run printf '%s\\n' 'hello from codex.' > hello.txt.", "agent": "billing", "urgency": "idle"},
        {"id": 1790127629500000, "ts": "2026-09-23T01:40:29.5Z", "kind": "push", "text": "docs has gone away; its pane closed.", "urgency": "interrupt"}
      ],
      "next_before": 1790065877490932
    }
    """#

    static let statePendingAction = #"""
    {
     "id": "ap-tlsnbh-1",
     "pane_id": "p-billing",
     "kind": "answer_prompt",
     "spoken_summary": "billing asks to run printf '%s\\n' 'hello from codex.' > hello.txt.",
     "payload_text": "",
     "created_at": "2026-09-22T18:40:29.424661-07:00",
     "expires_at": "2026-09-22T18:42:29.424661-07:00",
     "question": "would you like to run the following command",
     "command": "printf '%s\\n' 'hello from codex.' > hello.txt",
     "yes_keys": ["y"],
     "no_keys": ["esc"],
     "agent": "billing",
     "summary": "billing asks to run printf '%s\\n' 'hello from codex.' > hello.txt.",
     "options": [
      {"index": 1, "label": "Yes, proceed", "keys": ["y"], "default": true},
      {"index": 2, "label": "Yes, and don't ask again for commands that start with `printf '%s\\n' 'hello from codex.' > hello.txt`", "keys": ["p"], "default": false},
      {"index": 3, "label": "No, and tell Codex what to do differently", "keys": ["esc"], "default": false}
     ]
    }
    """#

    /// Raw `/events` lines, as the daemon wrote them.
    static let sseCreatedAndConfirmed = [
        "id: 1790127629425157",
        "event: log",
        #"data: {"id":1790127629425157,"ts":"2026-09-23T01:40:29.425157Z","source":"supervisor","type":"pending.created","pane_id":"p-billing","payload":{"agent":"billing","command":"printf '%s\\n' 'hello from codex.' > hello.txt","expires_at":"2026-09-22T18:42:29.424661-07:00","id":"ap-tlsnbh-1","kind":"answer_prompt","no_keys":["esc"],"options":[{"index":1,"label":"Yes, proceed","keys":["y"],"default":true},{"index":2,"label":"Yes, and don't ask again for commands that start with `printf '%s\\n' 'hello from codex.' > hello.txt`","keys":["p"],"default":false},{"index":3,"label":"No, and tell Codex what to do differently","keys":["esc"],"default":false}],"question":"would you like to run the following command","summary":"billing asks to run printf '%s\\n' 'hello from codex.' > hello.txt.","yes_keys":["y"]}}"#,
        "",
        "id: 1790127629426365",
        "event: log",
        #"data: {"id":1790127629426365,"ts":"2026-09-23T01:40:29.426365Z","source":"voice","type":"voice.push","payload":{"agent":"billing","scheduling_sent":"when_idle","skipped":true,"text":"billing asks to run printf '%s\\n' 'hello from codex.' > hello.txt.","urgency":"idle"}}"#,
        "",
        ": keepalive",
        "",
        "id: 1790127642099438",
        "event: log",
        #"data: {"id":1790127642099438,"ts":"2026-09-23T01:40:42.099438Z","source":"supervisor","type":"pending.confirmed","pane_id":"p-billing","payload":{"answer":"Yes, proceed","id":"ap-tlsnbh-1","keys":["y"],"option":1,"via":"tap"}}"#,
        "",
    ]
}
