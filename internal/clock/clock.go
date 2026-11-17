// Package clock separates display time from elapsed time that includes suspend.
// Platform sources are supplied by the application; tests can advance either clock.
package clock

import "time"

type Sample struct {
	Wall    time.Time
	Elapsed time.Duration
}

type Source func() Sample
