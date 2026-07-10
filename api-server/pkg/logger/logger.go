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

// MaskMSISDN masks a phone number to prevent exposing PII in logs.
// Example: +250788123456 -> +2507***
func MaskMSISDN(phone string) string {
	if len(phone) == 0 {
		return ""
	}
	if len(phone) >= 6 {
		return phone[:5] + "***"
	}
	return phone
}
