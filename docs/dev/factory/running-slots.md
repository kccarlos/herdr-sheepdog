# Running slots (`max_running`)

`max_running` caps how many tasks of a project run at once. A task uses a slot only while its worker is starting or working (`queues.py`, `working()`).

- **Stopped agent.** A running task whose agent terminal is gone ("Agent stopped") frees its slot. Resuming it takes the slot again, and `resume` is refused with a plain reason ("Can't resume X: N tasks are running here") when the cap is full.
- **Needs you.** A task whose row waits on the person or the coordinator (an open question, "Needs you") frees its slot too, as the stage table in `stage.py` names who a row waits on: the worker is not the one working, so it should not hold up queued work.
- **A message to a paused worker.** A live worker that is paused and gets a message from the person or the coordinator takes its slot back with no check. Refusing a message would lose the person's words, so the count may briefly pass `max_running`. The tick starts nothing new while the count is at or over the cap, so it settles as tasks finish.
