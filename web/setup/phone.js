// Pure helpers of the setup page's phone step (no DOM): `make test-web`.

// shellLAN is the "Shell on this network" switch's view of
// /api/setup/state phone: shown only once phone access is on, checked
// when web.shell_lan is on, and the line under it. Loopback and Tailscale
// always get the shell; the switch is only about the plain LAN.
export function shellLAN(phone) {
  const p = phone || {};
  return {
    show: !!p.enabled,
    checked: !!p.shell_lan,
    note: p.shell_lan
      ? 'On: a phone on this Wi-Fi can open a shell. Others on the network could read what you type there.'
      : 'Off: the phone shell works over Tailscale only. On plain Wi-Fi, others on the network could read what you type.',
  };
}

// shellLANSaved is the message after the switch was saved (r is the
// POST /api/setup/shell-lan answer).
export function shellLANSaved(r) {
  if (!r || r.error) return 'Not saved: ' + ((r && r.error) || 'no answer');
  return r.shell_lan ? 'Saved. The phone shell works on this network now.' : 'Saved. The phone shell is Tailscale-only again.';
}
