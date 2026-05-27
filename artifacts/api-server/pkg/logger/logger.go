package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

// New creates and returns the root zerolog logger.
// In development, pretty-prints to stdout.
// In production, emits JSON.
func New(env string) zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339

	if env == "development" {
		return zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
			With().
			Timestamp().
			Caller().
			Logger()
	}

	return zerolog.New(os.Stdout).
		With().
		Timestamp().
		Logger()
}
