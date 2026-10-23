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

// tsnetSwitch is the "Use a Tailscale certificate for browsers" switch
// (web.tailscale_https): shown once phone access is on and Tailscale is
// on this computer, off by default. The note says the cost first: the
// machine name goes into the public certificate logs.
export function tsnetSwitch(phone) {
  const p = phone || {};
  let note = 'Browsers on other computers open sheepdog with no warning. Tailscale\'s certificates are public: this computer\'s name and your tailnet\'s name get logged in public certificate logs. The app does not need it.';
  if (p.tailscale_https) {
    note = p.tailscale_name
      ? 'On: browsers open https://' + p.tailscale_name + ' without a warning. This name is in the public certificate logs.'
      : 'On, but there is no certificate yet' + (p.tailscale_error ? ': ' + p.tailscale_error : '.');
  }
  return { show: !!p.enabled && !!p.tailscale, checked: !!p.tailscale_https, note };
}

// tsnetSaved is the message after the switch was saved (r is the
// POST /api/setup/tailscale_https answer).
export function tsnetSaved(r) {
  if (!r || r.error) return 'Not turned on: ' + ((r && r.error) || 'no answer');
  return r.tailscale_https ? 'Saved. Browsers can open https://' + r.name + '.' : 'Saved. Browsers use the computer\'s own certificate again.';
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
