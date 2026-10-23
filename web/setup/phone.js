// Pure helpers of the setup page's phone step (no DOM): `make test-web`.

// shellSwitch is the "Shell from your phone" switch's view of
// /api/setup/state phone: shown only once phone access is on, checked
// when web.shell is on, and the line under it. One switch per computer,
// for every paired device on every network.
export function shellSwitch(phone) {
  const p = phone || {};
  return {
    show: !!p.enabled,
    checked: !!p.shell,
    note: p.shell
      ? 'On: your paired phones can open a shell and send attachments here.'
      : 'Off: no shell and no attachments from your phones. Agent windows still work.',
  };
}

// shellSaved is the message after the switch was saved (r is the
// POST /api/setup/shell answer).
export function shellSaved(r) {
  if (!r || r.error) return 'Not saved: ' + ((r && r.error) || 'no answer');
  return r.shell ? 'Saved. Your phones can open a shell.' : 'Saved. The phone shell is off.';
}

// countdown is the pairing code's remaining time as "9:41", or "" once it
// has expired (expires is an ISO time, now a Date or milliseconds).
export function countdown(expires, now) {
  const left = Math.floor((Date.parse(expires) - Number(now)) / 1000);
  if (!(left > 0)) return '';
  return Math.floor(left / 60) + ':' + String(left % 60).padStart(2, '0');
}

// deviceKind is a device row's kind label.
export function deviceKind(d) {
  switch (d && d.kind) {
    case 'ios': return 'iPhone';
    case 'ipados': return 'iPad';
    case 'browser': return 'Browser';
  }
  return 'Device';
}

// lastSeen says when and how a device last came in: "Wi-Fi · 5 min ago",
// "Tailscale · just now", "never".
export function lastSeen(d, now) {
  if (!d || !d.last_seen) return 'Not seen yet';
  const net = { tailscale: 'Tailscale', lan: 'Wi-Fi', loopback: 'this computer' }[d.last_network] || '';
  const s = Math.max(0, Math.floor((Number(now) - Date.parse(d.last_seen)) / 1000));
  let ago;
  if (s < 60) ago = 'just now';
  else if (s < 3600) ago = Math.floor(s / 60) + ' min ago';
  else if (s < 86400) ago = Math.floor(s / 3600) + ' h ago';
  else ago = Math.floor(s / 86400) + ' d ago';
  return net ? net + ' · ' + ago : ago;
}
