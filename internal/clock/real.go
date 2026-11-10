package clock

import "time"

// NewReal fails startup if the platform cannot provide suspend-aware elapsed time.
func NewReal() (Source, error) {
	if _, err := platformElapsed(); err != nil {
		return nil, err
	}
	return func() Sample {
		e, err := platformElapsed()
		if err != nil {
			return Sample{Elapsed: -1}
		}
		return Sample{Wall: time.Now().UTC(), Elapsed: e}
	}, nil
}
