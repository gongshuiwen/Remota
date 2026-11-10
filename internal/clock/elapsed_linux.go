package clock

import (
	"golang.org/x/sys/unix"
	"time"
)

func platformElapsed() (time.Duration, error) {
	var ts unix.Timespec
	err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts)
	return time.Duration(ts.Nano()), err
}
