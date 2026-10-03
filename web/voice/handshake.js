// The /ws protocol handshake, client side (docs/ws-contract.md "Protocol
// version and handshake"). Pure: app.js feeds it the socket's control
// messages and a timer, and acts on what it returns. No DOM, so
// `node --test` covers every transition (handshake.test.mjs).

/** The /ws protocol this page speaks. */
export const PROTOCOL = 1;
/** The oldest daemon protocol this page can work with. */
export const MIN_DAEMON_PROTOCOL = 1;
/** A daemon that has not answered client-ready by then is protocol 1. */
export const HANDSHAKE_TIMEOUT_MS = 2000;
/** The status reason a daemon refuses a too-old client with. */
export const REASON_TOO_OLD = 'protocol_too_old';

/**
 * The client-ready message: the first text frame on every connection.
 * The page is served by the daemon it talks to, so its version is
 * "embedded".
 */
export function clientReady() {
  return { type: 'client-ready', protocol: PROTOCOL, client: { name: 'sheepdog-voice', version: 'embedded', platform: 'web' } };
}

/**
 * One connection's handshake. state:
 *   waiting       sent client-ready, nothing back yet
 *   ready         bot-ready arrived: protocol, features, daemonVersion
 *   legacy        an older daemon (no bot-ready within the timeout, or it
 *                 answered client-ready with "unknown type"): protocol 1
 *   rejected      the daemon refused this page as too old (reload it)
 *   daemon_old    the daemon's protocol is below MIN_DAEMON_PROTOCOL
 */
export class Handshake {
  constructor() {
    this.state = 'waiting';
    this.protocol = null;
    this.features = [];
    this.daemonVersion = '';
  }

  /**
   * Feed one control message. Returns {consumed, close, message}:
   * consumed — the page should not handle it further (an old daemon's
   * "unknown type client-ready" error is not a problem to show); close —
   * stop and do not reconnect; message — words for the shepherd.
   */
  onMessage(m) {
    if (!m || typeof m !== 'object') return { consumed: false };
    if (m.type === 'bot-ready') {
      const p = Number.isInteger(m.protocol) ? m.protocol : 1;
      this.daemonVersion = (m.daemon && m.daemon.version) || '';
      this.features = Array.isArray(m.features) ? m.features.slice() : [];
      if (p < MIN_DAEMON_PROTOCOL) {
        this.state = 'daemon_old';
        this.protocol = p;
        return { consumed: true, close: true, message: `The daemon speaks /ws protocol ${p}; this page needs ${MIN_DAEMON_PROTOCOL} or newer. Update the daemon.` };
      }
      this.state = 'ready';
      this.protocol = Math.min(p, PROTOCOL);
      return { consumed: true };
    }
    if (m.type === 'error' && this.state === 'waiting' && typeof m.message === 'string' && m.message.includes('client-ready')) {
      this.legacy();
      return { consumed: true };
    }
    if (m.type === 'status' && m.voice === 'closed' && m.reason === REASON_TOO_OLD) {
      this.state = 'rejected';
      return { consumed: true, close: true, message: 'This page is older than the daemon: reload it to get the current one.' };
    }
    return { consumed: false };
  }

  /** The timer ran out: a daemon that never answers is protocol 1. */
  onTimeout() {
    if (this.state === 'waiting') this.legacy();
  }

  legacy() {
    this.state = 'legacy';
    this.protocol = 1;
  }

  /** "1 (daemon 0.4.2)", "1 (older daemon)", for the stats panel. */
  describe() {
    switch (this.state) {
      case 'waiting': return 'waiting for bot-ready';
      case 'ready': return `${this.protocol}${this.daemonVersion ? ` (daemon ${this.daemonVersion})` : ''}`;
      case 'legacy': return '1 (older daemon, no handshake)';
      case 'rejected': return 'refused: page too old';
      case 'daemon_old': return `daemon too old (${this.protocol})`;
      default: return this.state;
    }
  }
}
