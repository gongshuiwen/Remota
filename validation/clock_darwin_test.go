package validation

import (
	"golang.org/x/sys/unix"
	"time"
)

func clockName() string { return "CLOCK_MONOTONIC_RAW (continuous)" }
func elapsedNow() (time.Duration, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &ts); err != nil {
		return 0, err
	}
	return time.Duration(ts.Nano()), nil
}
