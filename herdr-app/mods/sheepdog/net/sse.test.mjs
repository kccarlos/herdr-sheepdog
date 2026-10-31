// The SSE client over a fake socket and a fake clock: `make test-panel`. The
// record strings are the ones ios/Tests/SheepdogKitTests/SSEParserTests.swift
// feeds Kit's SSEParser.
import { test } from "node:test";
import assert from "node:assert/strict";
import { BACKOFF_MAX_MS, decodeUtf8, Dechunker, eventsRequest, SILENCE_MS, SseClient, SseParser } from "./sse.js";

const enc = new TextEncoder();
const bytes = (s) => enc.encode(s);
const chunked = (s) => {
  const body = bytes(s);
  return `${body.length.toString(16)}\r\n${s}\r\n`;
};
const HEAD = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n";
const GREETING = ": sheepdog events 1.2.3\n\n";
const record = (id, type, payload = {}) => `id: ${id}\nevent: log\ndata: ${JSON.stringify({ ts: "2026-09-22T00:12:00.123456Z", source: "web", type, payload })}\n\n`;
const tick = async (n = 5) => {
  for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r));
};

// --- Dechunker ---

test("chunked bodies are taken apart wherever the socket cut them", () => {
  const wire = `${chunked("hello ")}${chunked("world")}0\r\n\r\n`;
  const whole = new Dechunker();
  assert.equal(decodeUtf8(Buffer.concat(whole.push(bytes(wire)))), "hello world");
  assert.equal(whole.done, true);
  // Every possible single cut, and one byte at a time.
  for (let cut = 1; cut < wire.length; cut++) {
    const d = new Dechunker();
    const out = [...d.push(bytes(wire.slice(0, cut))), ...d.push(bytes(wire.slice(cut)))];
    assert.equal(decodeUtf8(Buffer.concat(out)), "hello world", `cut at ${cut}`);
    assert.equal(d.done, true, `cut at ${cut}`);
  }
  const slow = new Dechunker();
  const out = [];
  for (const b of bytes(wire)) out.push(...slow.push(Uint8Array.of(b)));
  assert.equal(decodeUtf8(Buffer.concat(out)), "hello world");
});

test("a chunk size may be upper case, carry an extension and hold a multi-byte character", () => {
  const word = "né";
  const d = new Dechunker();
  const out = d.push(bytes(`${bytes(word).length.toString(16).toUpperCase()};name=value\r\n${word}\r\n0\r\nX-Trailer: 1\r\n\r\n`));
  assert.equal(decodeUtf8(Buffer.concat(out)), word);
  assert.equal(d.done, true);
});

test("a bad chunk size is an error, not silence", () => {
  assert.throws(() => new Dechunker().push(bytes("zz\r\nhi\r\n")), /bad chunk size/);
});

// --- SseParser ---

test("one event, with its id and payload; the greeting comment is liveness", () => {
  const out = new SseParser().push(bytes(`: sheepdog events 1.2.3\n\nid: 41\nevent: log\ndata: {"ts":"2026-09-22T00:12:00.123456Z","source":"supervisor","type":"pending.created","pane_id":"w1:p1","payload":{"question":"run the migration?"}}\n\n`));
  assert.equal(out.length, 2);
  assert.deepEqual(out[0], { type: "comment", text: "sheepdog events 1.2.3" });
  assert.equal(out[1].type, "event");
  assert.equal(out[1].id, 41);
  assert.equal(out[1].event.type, "pending.created");
  assert.equal(out[1].event.pane_id, "w1:p1");
  assert.equal(out[1].event.payload.question, "run the migration?");
});

test("keepalive comments are comments, several events in one stream are all delivered", () => {
  const out = new SseParser().push(bytes(`: keepalive\n\n: keepalive\n\n${record(1, "web.utterance", { text: "what's going on" })}${record(2, "voice.push", { text: "auth is asking.", skipped: true })}`));
  assert.deepEqual(out.map((r) => r.type), ["comment", "comment", "event", "event"]);
  assert.equal(out[3].event.payload.skipped, true);
});

test("closed carries its reason; undecodable data is reported, not swallowed", () => {
  const parser = new SseParser();
  assert.deepEqual(parser.push(bytes(`event: closed\ndata: {"reason":"shutdown"}\n\n`)), [{ type: "closed", reason: "shutdown" }]);
  assert.deepEqual(parser.push(bytes("id: 9\nevent: log\ndata: not json\n\n")), [{ type: "undecodable", data: "not json" }]);
});

test("a record split over two data lines is joined, and CRLF line ends read the same", () => {
  const out = new SseParser().push(bytes('event: log\r\ndata: {"source":"x","type":"y",\r\ndata: "payload":{"text":"ab"}}\r\n\r\n'));
  assert.equal(out[0].event.payload.text, "ab");
});

test("a record cut anywhere, even inside a character, comes out whole", () => {
  const wire = bytes(`${GREETING}${record(7, "voice.text", { text: "héllo 🐕" })}`);
  for (let cut = 1; cut < wire.length; cut++) {
    const parser = new SseParser();
    const out = [...parser.push(wire.slice(0, cut)), ...parser.push(wire.slice(cut))];
    assert.equal(out.length, 2, `cut at ${cut}`);
    assert.equal(out[1].event.payload.text, "héllo 🐕", `cut at ${cut}`);
  }
});

test("the request names the kinds and resumes after the last id both ways", () => {
  const first = eventsRequest({ authority: "127.0.0.1:7777", kinds: ["pending", "voice"], since: 0 });
  assert.match(first, /^GET \/events\?kinds=pending,voice HTTP\/1\.1\r\nHost: 127\.0\.0\.1:7777\r\n/);
  assert.doesNotMatch(first, /Last-Event-ID|since=/);
  assert.ok(first.endsWith("\r\n\r\n"));
  const resume = eventsRequest({ authority: "127.0.0.1:7777", kinds: ["pending"], since: 1790000000000001 });
  assert.match(resume, /\/events\?kinds=pending&since=1790000000000001 HTTP\/1\.1/);
  assert.match(resume, /\r\nLast-Event-ID: 1790000000000001\r\n/);
});

// --- SseClient ---

// A clock the test moves. sleep() waits for advance().
function fakeClock() {
  let now = 0;
  const timers = [];
  return {
    now: () => now,
    sleep(ms) {
      return new Promise((resolve) => timers.push({ at: now + ms, resolve }));
    },
    async advance(ms) {
      const target = now + ms;
      for (;;) {
        await tick(3);
        timers.sort((a, b) => a.at - b.at);
        if (!timers.length || timers[0].at > target) break;
        const timer = timers.shift();
        now = Math.max(now, timer.at);
        timer.resolve();
      }
      now = target;
      await tick(3);
    },
  };
}

// A socket the test feeds. Closing does not wake a pending read (the client
// must not depend on it); it only records the close.
function fakeSocket() {
  const queue = [];
  let waiting = null;
  return {
    written: "",
    closed: false,
    async write(data) {
      this.written += data;
    },
    read() {
      if (queue.length) return Promise.resolve(queue.shift());
      return new Promise((resolve) => {
        waiting = resolve;
      });
    },
    close() {
      this.closed = true;
    },
    feed(data) {
      const chunk = typeof data === "string" ? bytes(data) : data;
      if (waiting) {
        const resolve = waiting;
        waiting = null;
        resolve(chunk);
      } else queue.push(chunk);
    },
    eof() {
      this.feed(null);
    },
  };
}

// A client whose connections are the sockets `plan` gives in turn (an Error
// refuses the connection), recording what it did.
function harness({ plan, lastId = 0 }) {
  const clock = fakeClock();
  const log = { statuses: [], events: [], connects: 0, times: [], sockets: [] };
  const connect = async () => {
    log.times.push(clock.now());
    const next = plan[log.connects++] ?? new Error("refused");
    if (next instanceof Error) throw next;
    log.sockets.push(next);
    return next;
  };
  const client = new SseClient({
    connect, host: "127.0.0.1", port: 7777, authority: "127.0.0.1:7777", kinds: ["pending", "voice"], clock, lastId,
    onEvent: (event, id) => log.events.push({ type: event.type, id }),
    onStatus: (s) => log.statuses.push(s.state + (s.detail ? `: ${s.detail}` : "")),
  });
  const done = client.run();
  return { client, clock, log, done };
}

test("a stream is live from its first record, and events arrive once with their ids", async () => {
  const socket = fakeSocket();
  const { client, log, done } = harness({ plan: [socket] });
  await tick();
  assert.match(socket.written, /^GET \/events\?kinds=pending,voice HTTP\/1\.1\r\n/);
  assert.deepEqual(log.statuses, ["connecting"]);
  socket.feed(HEAD + chunked(GREETING));
  await tick();
  assert.deepEqual(log.statuses, ["connecting", "live"]);
  socket.feed(chunked(record(5, "pending.created")) + chunked(record(5, "pending.created")) + chunked(record(6, "voice.push")));
  await tick();
  assert.deepEqual(log.events, [{ type: "pending.created", id: 5 }, { type: "voice.push", id: 6 }]);
  client.stop();
  await done;
  assert.equal(socket.closed, true);
});

test("a chunk boundary inside a frame, and inside a chunk's size line, loses nothing", async () => {
  const socket = fakeSocket();
  const { client, log, done } = harness({ plan: [socket] });
  const wire = HEAD + chunked(GREETING) + chunked(record(5, "voice.text", { text: "héllo" })) + chunked(record(6, "voice.push"));
  const all = bytes(wire);
  // The socket hands over 7 bytes at a time: cuts land in the head, in size
  // lines, in the middle of data lines and of a multi-byte character.
  for (let i = 0; i < all.length; i += 7) {
    socket.feed(all.slice(i, i + 7));
    await tick(2);
  }
  assert.deepEqual(log.events, [{ type: "voice.text", id: 5 }, { type: "voice.push", id: 6 }]);
  client.stop();
  await done;
});

test("keepalive comments keep a quiet stream alive for as long as they come", async () => {
  const socket = fakeSocket();
  const { client, clock, log, done } = harness({ plan: [socket] });
  socket.feed(HEAD + chunked(GREETING));
  for (let i = 0; i < 12; i++) {
    await clock.advance(15_000);
    socket.feed(chunked(": keepalive\n\n"));
  }
  assert.equal(log.connects, 1);
  assert.equal(socket.closed, false);
  assert.deepEqual(log.statuses, ["connecting", "live"]);
  client.stop();
  await done;
});

test("no record for 40 s reconnects after 1 s, resuming with Last-Event-ID", async () => {
  const [first, second] = [fakeSocket(), fakeSocket()];
  const { client, clock, log, done } = harness({ plan: [first, second] });
  first.feed(HEAD + chunked(GREETING) + chunked(record(1790000000000042, "pending.created")));
  await clock.advance(SILENCE_MS - 1000);
  assert.equal(first.closed, false, "not yet");
  assert.equal(log.connects, 1);
  await clock.advance(1000);
  assert.equal(first.closed, true, "silent for 40 s");
  assert.equal(log.statuses.at(-1), "down");
  assert.equal(log.connects, 1, "waiting out the backoff");
  await clock.advance(1000);
  assert.equal(log.connects, 2);
  assert.match(second.written, /\/events\?kinds=pending,voice&since=1790000000000042 HTTP\/1\.1/);
  assert.match(second.written, /\r\nLast-Event-ID: 1790000000000042\r\n/);
  second.feed(HEAD + chunked(GREETING) + chunked(record(1790000000000043, "voice.push")));
  await tick();
  assert.deepEqual(log.events.map((e) => e.id), [1790000000000042, 1790000000000043]);
  assert.equal(log.statuses.at(-1), "live");
  client.stop();
  await done;
});

test("the backoff doubles from 1 s to 15 s while nothing connects, and a stream that delivers resets it", async () => {
  const live = fakeSocket();
  const { client, clock, log, done } = harness({ plan: [new Error("refused"), new Error("refused"), new Error("refused"), new Error("refused"), new Error("refused"), new Error("refused"), live] });
  for (let t = 0; t < 60_000 && log.connects < 7; t += 500) await clock.advance(500);
  // Attempts at 0, then after 1, 2, 4, 8, 15 and 15 s.
  const gaps = log.times.slice(1).map((t, i) => t - log.times[i]);
  assert.deepEqual(gaps, [1000, 2000, 4000, 8000, BACKOFF_MAX_MS, BACKOFF_MAX_MS]);
  assert.equal(log.statuses.filter((s) => s.startsWith("down: refused")).length, 6);
  // It connects, delivers, then drops: the next wait is 1 s again.
  live.feed(HEAD + chunked(GREETING));
  await tick();
  live.eof();
  await tick();
  const before = log.connects;
  await clock.advance(999);
  assert.equal(log.connects, before);
  await clock.advance(1);
  assert.equal(log.connects, before + 1);
  client.stop();
  await done;
});

test("an error status is down with its status, and is retried", async () => {
  const bad = fakeSocket();
  const good = fakeSocket();
  const { client, clock, log, done } = harness({ plan: [bad, good] });
  bad.feed("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n");
  await tick();
  assert.equal(log.statuses.at(-1), "down: HTTP 400");
  assert.equal(bad.closed, true);
  await clock.advance(1000);
  assert.equal(log.connects, 2);
  client.stop();
  await done;
});

test("the daemon saying closed, or the socket reaching EOF, reconnects", async () => {
  const [a, b, c] = [fakeSocket(), fakeSocket(), fakeSocket()];
  const { client, clock, log, done } = harness({ plan: [a, b, c] });
  a.feed(HEAD + chunked(GREETING) + chunked('event: closed\ndata: {"reason":"shutdown"}\n\n'));
  await clock.advance(1000);
  assert.equal(log.connects, 2);
  assert.equal(a.closed, true);
  b.feed(HEAD + chunked(GREETING));
  await tick();
  b.eof();
  await clock.advance(1000);
  assert.equal(log.connects, 3);
  client.stop();
  await done;
});

test("a body that is not chunked is read as it comes", async () => {
  const socket = fakeSocket();
  const { client, log, done } = harness({ plan: [socket] });
  socket.feed("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n" + GREETING + record(3, "voice.push"));
  await tick();
  assert.deepEqual(log.events, [{ type: "voice.push", id: 3 }]);
  client.stop();
  await done;
});

test("stop() ends a run that is waiting on a read that never returns", async () => {
  const socket = fakeSocket();
  const { client, log, done } = harness({ plan: [socket] });
  socket.feed(HEAD + chunked(GREETING));
  await tick();
  client.stop();
  await done;
  assert.equal(socket.closed, true);
  assert.equal(log.connects, 1);
});
