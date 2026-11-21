package sandbox

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"aegis/internal/config"
)

func TestFits(t *testing.T) {
	s := config.Sandbox{MaxConcurrent: 16, MemoryMB: 1024, HostReserveMB: 6144}
	if err := fits(s, 32768); err != nil {
		t.Fatalf("16 × 1 GB + 6 GB should fit 32 GB: %v", err)
	}
	if err := fits(s, 16384); err == nil {
		t.Fatal("16 × 1 GB + 6 GB must not fit 16 GB")
	}
}

func TestSlotsCapConcurrency(t *testing.T) {
	slots := NewSlots(2)
	var running, peak int32
	done := make(chan struct{})
	for i := 0; i < 6; i++ {
		go func() {
			slots <- struct{}{}
			n := atomic.AddInt32(&running, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&running, -1)
			<-slots
			done <- struct{}{}
		}()
	}
	for i := 0; i < 6; i++ {
		<-done
	}
	if peak > 2 {
		t.Fatalf("peak concurrency %d, limit 2", peak)
	}
}

func TestRunWaitsForSlotAndHonoursCancel(t *testing.T) {
	d := Docker{Slots: NewSlots(1)}
	d.Slots <- struct{}{} // the only slot is taken
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := d.Run(ctx, Spec{Image: "x", Cmd: []string{"true"}, Timeout: time.Second}); err == nil {
		t.Fatal("Run should give up when the context ends while waiting for a slot")
	}
}
