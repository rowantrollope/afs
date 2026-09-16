// Package display formats human-facing output without changing stored values.
package display

import (
	"io"
	"time"
)

// Time renders a timestamp in the system's configured local timezone.
func Time(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("02/01/2006 03:04:05 PM")
}

// LogWriter adds a human-readable timestamp to standard logger messages.
// Use with log.SetFlags(0) to avoid a second, differently formatted timestamp.
type LogWriter struct{ io.Writer }

func (w LogWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(w.Writer, Time(time.Now())+" "); err != nil {
		return 0, err
	}
	return w.Writer.Write(p)
}
