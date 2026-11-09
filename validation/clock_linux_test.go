package validation

import (
	"golang.org/x/sys/unix"
	"time"
)

func clockName() string { return "CLOCK_BOOTTIME" }
func elapsedNow() (time.Duration, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return 0, err
	}
	return time.Duration(ts.Nano()), nil
}
