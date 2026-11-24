# Owner contract v1 (superseded)

Owner contract v1 was the JSON boundary between the sheepdog daemon and the first factory engine. That engine is gone, and the factory engine now runs inside the daemon, so there is no wire contract between the two.

| For | Read |
|---|---|
| What each surface reads, and the rule that the Go types are the only source of the shapes | [design.md 12.1](../engine-v2/design.md#121-what-the-surfaces-read) |
| The engine's Go interface | [design.md 12.3](../engine-v2/design.md#123-the-engines-go-interface) |
| Asks, owner answers and the answer proof | design.md 7.1 and 7.2 |
| Alerts and the quiet rule | design.md section 6 |

The full v1 text is in git history: `git show 93f0d6dd:docs/dev/factory/owner-contract.md`. Line numbers that [knowledge-base.md](../engine-v2/knowledge-base.md) cites for this file refer to that commit.
