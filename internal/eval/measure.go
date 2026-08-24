package eval

import "fmt"

const repeatMax = 100

func normalizeRepeat(repeat int) (int, error) {
	if repeat == 0 {
		return 1, nil
	}
	if repeat < 0 {
		return 0, fmt.Errorf("repeat must not be negative, got %d", repeat)
	}
	if repeat > repeatMax {
		return 0, fmt.Errorf("repeat must not exceed %d, got %d", repeatMax, repeat)
	}
	return repeat, nil
}

func measurementPhase(repetition int) string {
	if repetition == 1 {
		return "cold"
	}
	return "warm"
}
