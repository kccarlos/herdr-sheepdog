# Review rounds

Each review round records its verdict, major and blocker count, findings, commit, reviewer CLI and model. A worker records its own review with `herdr-task reviewed --verdict pass|changes --majors N --report "findings"`; the engine records rounds it starts itself. Older free-text rounds remain in the history but cannot establish a major count.

Once the normal rounds are used, the coordinator can grant `review-more` once per task. From round three, two consecutive rounds that request changes and report majors stop the task for a coordinator recap; `review-more` and recap `--decide continue` are refused. The coordinator can choose a split, a new approach or dropping the task. Merge-only reviews do not count toward this rule. Size, time, token and CI triggers still ask the worker for a recap.
