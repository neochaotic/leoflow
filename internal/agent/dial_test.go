package agent

import "testing"

func TestDialRequiresAddressAndToken(t *testing.T) {
	if _, _, err := Dial("", "token", true, ""); err == nil {
		t.Error("missing address should error")
	}
	if _, _, err := Dial("localhost:50051", "", true, ""); err == nil {
		t.Error("missing token should error")
	}
}

func TestDialBuildsClient(t *testing.T) {
	for _, insecure := range []bool{true, false} {
		client, conn, err := Dial("localhost:50051", "token", insecure, "")
		if err != nil {
			t.Fatalf("Dial(insecure=%v): %v", insecure, err)
		}
		if client == nil || conn == nil {
			t.Fatalf("Dial(insecure=%v) returned nil client/conn", insecure)
		}
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("closing conn: %v", cerr)
		}
	}
}
