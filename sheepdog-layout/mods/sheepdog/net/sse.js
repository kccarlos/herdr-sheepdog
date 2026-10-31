// A hand-written SSE client for the daemon's `GET /events`, over the mod
// runtime's raw TCP socket (docs/dev/design/desktop-client.md, section 6.2):
// `fetch` there is buffered (8 MiB, 30 s), so a stream cannot use it.
//
// Layers, each testable on its own: `Dechunker` takes the HTTP/1.1 chunked
// body apart, `SseParser` reads id:/event:/data: records out of it (it mirrors
// ios/Sources/SheepdogKit/SSEParser.swift), `SseClient` sends the request,
// treats every record, `: keepalive` comments included, as proof the stream is
// alive, and reconnects when it goes quiet. No herdr imports: `connect` and
// the clock are passed in, so node tests it with a fake socket.

// The daemon sends `: keepalive` every 15 s. No record for this long and the
// stream is dead, whatever the socket says.
export const SILENCE_MS = 40_000;
// How often the silence is checked.
export const WATCH_TICK_MS = 1000;
// Reconnect delays: 1 s, doubling to 15 s; a stream that delivers resets it.
export const BACKOFF_START_MS = 1000;
export const BACKOFF_MAX_MS = 15_000;

const CR = 13;
const LF = 10;

function concat(a, b) {
  if (a.length === 0) return b;
  const out = new Uint8Array(a.length + b.length);
  out.set(a, 0);
  out.set(b, a.length);
  return out;
}

// UTF-8 to a string. A record is cut at its blank line, which is ASCII, so a
// multi-byte character is never split across what this is given. The runtime
// may have no TextDecoder, so it is done by hand; a bad sequence is U+FFFD.
export function decodeUtf8(bytes) {
  let out = "";
  for (let i = 0; i < bytes.length; ) {
    const b = bytes[i];
    let cp = 0xfffd;
    let n = 1;
    if (b < 0x80) {
      cp = b;
    } else if (b >= 0xc2 && b < 0xe0 && (bytes[i + 1] & 0xc0) === 0x80) {
      cp = ((b & 0x1f) << 6) | (bytes[i + 1] & 0x3f);
      n = 2;
    } else if (b >= 0xe0 && b < 0xf0 && (bytes[i + 1] & 0xc0) === 0x80 && (bytes[i + 2] & 0xc0) === 0x80) {
      cp = ((b & 0x0f) << 12) | ((bytes[i + 1] & 0x3f) << 6) | (bytes[i + 2] & 0x3f);
      n = 3;
    } else if (b >= 0xf0 && b < 0xf5 && (bytes[i + 1] & 0xc0) === 0x80 && (bytes[i + 2] & 0xc0) === 0x80 && (bytes[i + 3] & 0xc0) === 0x80) {
      cp = ((b & 0x07) << 18) | ((bytes[i + 1] & 0x3f) << 12) | ((bytes[i + 2] & 0x3f) << 6) | (bytes[i + 3] & 0x3f);
      n = 4;
    }
    if (cp > 0x10ffff || (cp >= 0xd800 && cp < 0xe000)) cp = 0xfffd;
    out += String.fromCodePoint(cp);
    i += n;
  }
  return out;
}

// HTTP/1.1 chunked transfer decoding: `<hex size>[;ext]\r\n<data>\r\n`, ended
// by a zero-size chunk. `push(bytes)` takes whatever the socket gave, cut
// anywhere (inside a size line, inside the data, between the data and its
// CRLF), and returns the body bytes it completed.
export class Dechunker {
  constructor() {
    this.buf = new Uint8Array(0);
    this.left = 0; // data bytes still owed by the current chunk
    this.state = "size"; // size | data | crlf | trailer | done
  }

  get done() {
    return this.state === "done";
  }

  push(bytes) {
    this.buf = concat(this.buf, bytes);
    const out = [];
    for (;;) {
      if (this.state === "size") {
        const end = this.buf.indexOf(LF);
        if (end < 0) break;
        const line = decodeUtf8(this.buf.subarray(0, end)).replace(/\r$/, "").split(";")[0].trim();
        this.buf = this.buf.subarray(end + 1);
        if (!/^[0-9a-f]+$/i.test(line)) throw new Error(`bad chunk size ${JSON.stringify(line)}`);
        this.left = parseInt(line, 16);
        this.state = this.left === 0 ? "trailer" : "data";
      } else if (this.state === "data") {
        if (this.buf.length === 0) break;
        const take = Math.min(this.left, this.buf.length);
        out.push(this.buf.slice(0, take));
        this.buf = this.buf.subarray(take);
        this.left -= take;
        if (this.left === 0) this.state = "crlf";
      } else if (this.state === "crlf") {
        // The CRLF after a chunk's data.
        const need = this.buf[0] === CR ? 2 : 1;
        if (this.buf.length < need) break;
        this.buf = this.buf.subarray(need);
        this.state = "size";
      } else if (this.state === "trailer") {
        // After the last chunk: optional trailer lines, then an empty line.
        const end = this.buf.indexOf(LF);
        if (end < 0) break;
        const empty = end === 0 || (end === 1 && this.buf[0] === CR);
        this.buf = this.buf.subarray(end + 1);
        if (empty) this.state = "done";
      } else {
        break;
      }
    }
    return out;
  }
}

// Where a record ends: the index just past a blank line (LF LF, or CRLF CRLF),
// searching from `from`; -1 if the record is not complete yet.
function recordEnd(buf, from) {
  for (let i = from; i < buf.length; i++) {
    if (buf[i] !== LF) continue;
    let j = i + 1;
    if (buf[j] === CR) j++;
    if (buf[j] === LF) return j + 1;
  }
  return -1;
}

// Reads the daemon's `/events` records out of bytes, the way Kit's SSEParser
// reads lines. `push(bytes)` returns what the bytes completed:
//   { type: "event", id, event }  `event` is the daemon's JSON (type, ts,
//                                  source, pane_id, payload); `id` its id:
//   { type: "comment", text }     `: keepalive` and the greeting: liveness
//   { type: "closed", reason }    the daemon ended the stream on purpose
//   { type: "undecodable", data } a data: line that was not JSON
export class SseParser {
  constructor() {
    this.buf = new Uint8Array(0);
  }

  push(bytes) {
    this.buf = concat(this.buf, bytes);
    const out = [];
    for (let end = recordEnd(this.buf, 0); end >= 0; end = recordEnd(this.buf, 0)) {
      const record = this.record(decodeUtf8(this.buf.subarray(0, end)));
      this.buf = this.buf.subarray(end);
      if (record) out.push(record);
    }
    return out;
  }

  // One record's lines. A record of only comments is a comment; no data means
  // nothing to deliver.
  record(text) {
    let id = 0;
    let name = "";
    let data = "";
    let comment = null;
    for (const raw of text.split("\n")) {
      const line = raw.replace(/\r$/, "");
      if (line === "") continue;
      if (line.startsWith(":")) {
        comment = line.slice(1).trim();
        continue;
      }
      const colon = line.indexOf(":");
      const field = colon < 0 ? line : line.slice(0, colon);
      let value = colon < 0 ? "" : line.slice(colon + 1);
      if (value.startsWith(" ")) value = value.slice(1);
      if (field === "id") id = Math.max(id, Number(value) || 0);
      else if (field === "event") name = value;
      else if (field === "data") data += (data ? "\n" : "") + value;
    }
    if (!data) return comment == null ? null : { type: "comment", text: comment };
    let parsed;
    try {
      parsed = JSON.parse(data);
    } catch {
      return { type: "undecodable", data };
    }
    if (name === "closed") return { type: "closed", reason: parsed?.reason ?? "closed" };
    if (!parsed || typeof parsed.type !== "string") return { type: "undecodable", data };
    return { type: "event", id, event: parsed };
  }
}

// The request line and headers for `/events`: the event-type prefixes the
// panel shows, resuming after `since` (both `?since=` and `Last-Event-ID`, as
// Kit's eventsRequest does; the header wins on the daemon).
export function eventsRequest({ authority, kinds, since }) {
  const query = [];
  if (kinds?.length) query.push(`kinds=${kinds.join(",")}`);
  if (since > 0) query.push(`since=${since}`);
  const lines = [
    `GET /events${query.length ? `?${query.join("&")}` : ""} HTTP/1.1`,
    `Host: ${authority}`,
    "Accept: text/event-stream",
    "Cache-Control: no-cache",
    "Connection: keep-alive",
  ];
  if (since > 0) lines.push(`Last-Event-ID: ${since}`);
  return `${lines.join("\r\n")}\r\n\r\n`;
}

// The response head: status and headers (names lowercased), and the body bytes
// already received after it. Null until the blank line has arrived.
function parseHead(buf) {
  for (let i = 0; i + 3 < buf.length; i++) {
    if (buf[i] === CR && buf[i + 1] === LF && buf[i + 2] === CR && buf[i + 3] === LF) {
      const lines = decodeUtf8(buf.subarray(0, i)).split("\r\n");
      const status = Number(/^HTTP\/1\.[01] (\d{3})/.exec(lines[0])?.[1] ?? 0);
      const headers = {};
      for (const line of lines.slice(1)) {
        const colon = line.indexOf(":");
        if (colon > 0) headers[line.slice(0, colon).trim().toLowerCase()] = line.slice(colon + 1).trim();
      }
      return { status, headers, rest: buf.subarray(i + 4) };
    }
  }
  return null;
}

// Keeps one `/events` stream up.
//   connect(host, port)  the runtime's `net.connect`
//   clock                { now(), sleep(ms) }
//   onEvent(event, id)   a daemon event, once per id
//   onStatus(status)     { state: "connecting" | "live" | "down", detail }
//   lastId               resume after this event id
// `run()` resolves when `stop()` is called. A stream is "live" from its first
// record; a connection that never delivers one is retried with the backoff.
export class SseClient {
  constructor({ connect, host, port, authority, kinds, clock, onEvent, onStatus, lastId = 0 }) {
    Object.assign(this, { connect, host, port, authority, kinds, clock, onEvent, onStatus, lastId });
    this.stopped = false;
    this.socket = null;
    // Settles when stop() is called, to wake whatever the client waits on.
    this.stopSignal = new Promise((resolve) => {
      this.wake = resolve;
    });
  }

  stop() {
    this.stopped = true;
    this.wake();
    try {
      this.socket?.close();
    } catch {
      // Already closed.
    }
  }

  async run() {
    let backoff = BACKOFF_START_MS;
    while (!this.stopped) {
      this.onStatus({ state: "connecting", detail: "" });
      let detail = "";
      try {
        const delivered = await this.session();
        if (delivered) backoff = BACKOFF_START_MS;
      } catch (e) {
        detail = e?.message ?? String(e);
      }
      if (this.stopped) break;
      this.onStatus({ state: "down", detail });
      await Promise.race([this.clock.sleep(backoff), this.stopSignal]);
      backoff = Math.min(backoff * 2, BACKOFF_MAX_MS);
    }
  }

  // One connection, until it ends, goes quiet or is stopped. Resolves whether
  // it delivered a record; throws when it could not even start.
  async session() {
    const socket = await this.connect(this.host, this.port);
    this.socket = socket;
    let delivered = false;
    let quiet = false;
    let over = false;
    let lastRecord = this.clock.now();
    // The watchdog: a task of its own, because a read that never returns
    // cannot be given a timeout. It wakes the read race when the stream has
    // said nothing for SILENCE_MS.
    let trip;
    const tripped = new Promise((resolve) => {
      trip = resolve;
    });
    const watch = async () => {
      while (!over && !this.stopped) {
        await this.clock.sleep(WATCH_TICK_MS);
        if (!over && this.clock.now() - lastRecord >= SILENCE_MS) {
          quiet = true;
          trip();
          return;
        }
      }
    };
    watch();
    try {
      await socket.write(eventsRequest({ authority: this.authority, kinds: this.kinds, since: this.lastId }));
      let head = null;
      let headBuf = new Uint8Array(0);
      let body = null; // a Dechunker when the body is chunked
      const parser = new SseParser();
      while (!this.stopped && !quiet) {
        const chunk = await Promise.race([socket.read(), tripped, this.stopSignal]);
        // EOF (null), the watchdog or stop() (both undefined).
        if (chunk == null || this.stopped) break;
        let bytes = chunk;
        if (!head) {
          headBuf = concat(headBuf, bytes);
          head = parseHead(headBuf);
          if (!head) continue;
          if (head.status !== 200) throw new Error(`HTTP ${head.status}`);
          if ((head.headers["transfer-encoding"] ?? "").toLowerCase().includes("chunked")) body = new Dechunker();
          bytes = head.rest;
        }
        const parts = body ? body.push(bytes) : [bytes];
        for (const part of parts) {
          for (const record of parser.push(part)) {
            lastRecord = this.clock.now();
            if (!delivered) {
              delivered = true;
              this.onStatus({ state: "live", detail: "" });
            }
            if (record.type === "closed") return delivered;
            if (record.type === "event") this.deliver(record);
          }
        }
        if (body?.done) break;
      }
      return delivered;
    } finally {
      over = true;
      this.socket = null;
      try {
        socket.close();
      } catch {
        // Already closed.
      }
    }
  }

  // An event is handed over once; a replay after a reconnect can repeat ids.
  deliver({ id, event }) {
    if (id > 0 && id <= this.lastId) return;
    if (id > 0) this.lastId = id;
    this.onEvent(event, id);
  }
}
