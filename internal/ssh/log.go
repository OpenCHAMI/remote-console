// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh

import "time"

const consoleLogTimestampFormat = "2006-01-02 15:04:05 "

// consoleLogLineState tracks line boundaries that may span SSH reads.
type consoleLogLineState uint8

const (
	consoleLogLineInit consoleLogLineState = iota // No line data has been written.
	consoleLogLineData                            // The current line contains data.
	consoleLogLineCR                              // A carriage return may be followed by a line feed.
	consoleLogLineLF                              // The previous line ended.
)

// consoleLogFormatter matches ConMan line timestamps and sanitization.
type consoleLogFormatter struct {
	state consoleLogLineState
}

func newConsoleLogFormatter() consoleLogFormatter {
	return consoleLogFormatter{}
}

func (f *consoleLogFormatter) reset() {
	f.state = consoleLogLineInit
}

func (f *consoleLogFormatter) format(data []byte, now time.Time) []byte {
	if len(data) == 0 {
		return nil
	}

	timestamp := now.Format(consoleLogTimestampFormat)
	formatted := make([]byte, 0, len(data)+len(timestamp))

	// Preserve line state because SSH reads may split a line ending.
	for _, b := range data {
		switch b {
		case '\r':
			// Hold a carriage return until the next byte identifies the line ending.
			switch f.state {
			case consoleLogLineData:
				f.state = consoleLogLineCR
			case consoleLogLineInit:
				formatted = append(formatted, timestamp...)
				f.state = consoleLogLineCR
			}

		case '\n':
			// Normalize line endings and mark the next byte as a new line.
			if f.state == consoleLogLineInit || f.state == consoleLogLineLF {
				formatted = append(formatted, timestamp...)
			}
			formatted = append(formatted, '\r', '\n')
			f.state = consoleLogLineLF

		case 0:
			// ConMan ignores a NUL immediately after a line ending.
			if f.state == consoleLogLineCR || f.state == consoleLogLineLF {
				continue
			}
			formatted = f.appendByte(formatted, b, timestamp)

		default:
			formatted = f.appendByte(formatted, b, timestamp)
		}
	}

	return formatted
}

func (f *consoleLogFormatter) appendByte(dst []byte, b byte, timestamp string) []byte {
	// Complete a lone carriage return as a normalized line ending.
	if f.state == consoleLogLineCR {
		dst = append(dst, '\r', '\n')
	}

	// Timestamp only the first byte of each logical line.
	if f.state != consoleLogLineData {
		dst = append(dst, timestamp...)
	}
	f.state = consoleLogLineData

	// Match ConMan caret and meta notation, which differs from strconv quoting.
	c := b & 0x7f
	switch {
	case c < 0x20:
		prefix := byte('^')
		if b&0x80 != 0 {
			prefix = '~'
		}
		return append(dst, prefix, c+'@')
	case c == 0x7f:
		prefix := byte('^')
		if b&0x80 != 0 {
			prefix = '~'
		}
		return append(dst, prefix, '?')
	default:
		if b&0x80 != 0 {
			dst = append(dst, '`')
		}
		return append(dst, c)
	}
}
