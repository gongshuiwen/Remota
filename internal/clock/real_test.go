package clock

import (
	"runtime"
	"testing"
	"time"
)

func TestRealElapsedClock(t *testing.T) {
	source, err := NewReal()
	if err != nil {
		t.Fatal(err)
	}
	first := source()
	limit := time.Now().Add(time.Second)
	for time.Now().Before(limit) {
		current := source()
		if current.Wall.IsZero() || current.Elapsed < first.Elapsed {
			t.Fatal("elapsed clock is unavailable or moved backwards")
		}
		if current.Elapsed > first.Elapsed {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("elapsed clock did not advance")
}
