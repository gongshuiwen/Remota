package clock

import (
	"errors"
	"golang.org/x/sys/windows"
	"time"
	"unsafe"
)

var interruptTime = windows.NewLazySystemDLL("api-ms-win-core-realtime-l1-1-1.dll").NewProc("QueryInterruptTimePrecise")

func platformElapsed() (time.Duration, error) {
	if err := interruptTime.Find(); err != nil {
		return 0, err
	}
	var ticks uint64
	interruptTime.Call(uintptr(unsafe.Pointer(&ticks)))
	if ticks > uint64((1<<63-1)/100) {
		return 0, errors.New("elapsed time overflow")
	}
	return time.Duration(ticks * 100), nil
}
