package agent

import "testing"

func TestDialRequiresAddressAndToken(t *testing.T) {
	if _, _, _, err := Dial("", "token", true, ""); err == nil {
		t.Error("missing address should error")
	}
	if _, _, _, err := Dial("localhost:50051", "", true, ""); err == nil {
		t.Error("missing token should error")
	}
}

func TestDialBuildsClient(t *testing.T) {
	for _, insecure := range []bool{true, false} {
		client, conn, tokens, err := Dial("localhost:50051", "token", insecure, "")
		if err != nil {
			t.Fatalf("Dial(insecure=%v): %v", insecure, err)
		}
		if client == nil || conn == nil || tokens == nil {
			t.Fatalf("Dial(insecure=%v) returned nil client/conn/tokens", insecure)
		}
		if tokens.Token() != "token" {
			t.Errorf("Dial seeded token = %q, want %q", tokens.Token(), "token")
		}
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("closing conn: %v", cerr)
		}
	}
}
