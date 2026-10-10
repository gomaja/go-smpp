package gosmpp

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

// BenchmarkFullWindowWrite uses the tracked-write transport and settings with
// every permitted slot occupied by a written legacy reservation.
func BenchmarkFullWindowWrite(b *testing.B) {
	for _, occupancy := range []int{10, 100, 255} {
		b.Run(fmt.Sprint(occupancy), func(b *testing.B) {
			conn, cleanup := newPipePair()
			defer cleanup()
			settings := lifecycleSettings()
			settings.MaxWindowSize = uint8(occupancy)
			store := NewDefaultStore()
			out := newTransmittable(conn, settings, store)
			out.trackRequestOwner()
			defer func() { _ = out.close(ExplicitClosing) }()
			ctx := context.Background()
			for range occupancy {
				request := Request{PDU: pdu.NewSubmitSM(), TimeSent: time.Now(), WrittenAt: time.Now()}
				if err := store.Set(ctx, request); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := out.write(pdu.NewSubmitSM()); !errors.Is(err, ErrWindowsFull) {
					b.Fatalf("saturated window error = %v, want ErrWindowsFull", err)
				}
			}
			b.StopTimer()
		})
	}
}
