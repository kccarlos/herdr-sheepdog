// Work that outlives a click. A handler's `cx` speaks for that one host call
// and is stale after the first `await` ("cx is no longer valid: it was
// captured during an earlier call and used later"). `cx.spawn` hands its body
// an async context that stays valid across awaits, so every handler that
// awaits runs its async part through `later` and uses only the `cx` it is
// given. No herdr imports.

export function later(cx, body) {
  return cx.spawn((acx) => body(acx));
}
