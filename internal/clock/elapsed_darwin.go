package clock

import (
	"golang.org/x/sys/unix"
	"time"
)

func platformElapsed() (time.Duration, error) {
	var ts unix.Timespec
	err := unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &ts)
	return time.Duration(ts.Nano()), err
}
