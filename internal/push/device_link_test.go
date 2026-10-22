package push

import (
	"strings"
	"testing"
)

// Registrations belong to paired devices: the new-device push skips the
// new device, a device re-registering replaces its old token, and
// revoking a device drops its registrations. Against the relay's fake.
func TestRelayDeviceLinkedRegistrations(t *testing.T) {
	f := newFakeRelay(t)
	s, _ := newRelaySender(t, f, t.TempDir())
	if _, err := s.RegisterDevice(Registration{Token: tokA, Environment: EnvSandbox, Name: "iPhone", DeviceID: "dev-old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterDevice(Registration{Token: tokB, Environment: EnvSandbox, Name: "iPad", DeviceID: "dev-new"}); err != nil {
		t.Fatal(err)
	}
	s.Notify(Notification{EventID: "device-paired:dev-new", Kind: KindDevicePaired, Text: "New device paired to Studio. Not you? Open the computer's setup page to revoke.", NeedsYou: true, ExceptDevice: "dev-new"})
	s.Wait()
	if len(f.sends) != 1 || f.sends[0].Payload.Kind != KindDevicePaired {
		t.Fatalf("sends = %+v, want one device_paired push to the other device", f.sends)
	}
	// A new token for the same device replaces the old registration.
	tokC := strings.Repeat("c", 58) + "654321"
	if _, err := s.RegisterDevice(Registration{Token: tokC, Environment: EnvSandbox, Name: "iPhone", DeviceID: "dev-old"}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Devices()); n != 2 {
		t.Fatalf("devices = %d, want 2 (one per paired device)", n)
	}
	if err := s.ForgetDevices([]string{"dev-old"}); err != nil {
		t.Fatal(err)
	}
	if d := s.Devices(); len(d) != 1 || d[0].DeviceID != "dev-new" {
		t.Errorf("after revoke = %+v", d)
	}
}
