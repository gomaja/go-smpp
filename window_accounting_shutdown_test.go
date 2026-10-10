package gosmpp

import "testing"

func TestWindowSizeAfterUnstartedCloseDoesNotRegisterOwner(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	v := newTransceivable(conn, lifecycleSettings(), NewDefaultStore())
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if requestOwnerAlive(v.out.owner) {
		t.Fatal("closed unused generation registered")
	}
	if size, err := v.GetWindowSize(); err != nil || size != 0 {
		t.Fatalf("closed window: size=%d error=%v", size, err)
	}
	if requestOwnerAlive(v.out.owner) {
		t.Fatal("window query registered a closed generation")
	}
}
