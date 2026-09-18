package voice

import "testing"

func TestStringers(t *testing.T) {
	if UrgencyIdle.String() != "idle" || UrgencyInterrupt.String() != "interrupt" || UrgencySilent.String() != "silent" || Urgency(9).String() != "unknown" {
		t.Error("urgency names")
	}
	if StatusConnecting.String() != "connecting" || StatusConnected.String() != "connected" || StatusReconnecting.String() != "reconnecting" || StatusClosed.String() != "closed" || Status(9).String() != "unknown" {
		t.Error("status names")
	}
	if DirIn.String() != "in" || DirOut.String() != "out" {
		t.Error("direction names")
	}
	var u Urgency
	if u != UrgencyIdle {
		t.Error("zero urgency must be idle")
	}
}
