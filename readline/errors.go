package readline

import (
	"errors"
)

var (
	ErrInterrupt  = errors.New("Interrupt")
	ErrEditPrompt = errors.New("EditPrompt")
	// ErrNoData is returned when the terminal fd (set to O_NONBLOCK) has no
	// pending input. Used by auto-send logic in Readline().
	ErrNoData = errors.New("no data available")
)

type InterruptError struct {
	Line []rune
}

func (*InterruptError) Error() string {
	return "Interrupted"
}
