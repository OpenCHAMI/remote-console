// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package ssh

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestConsoleLogFormatter(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 34, 56, 0, time.UTC)
	timestamp := "2026-07-31 12:34:56 "

	tests := []struct {
		name  string
		input []byte
		want  []byte
	}{
		{
			name:  "timestamps lines",
			input: []byte("first\r\nsecond\n"),
			want:  []byte(timestamp + "first\r\n" + timestamp + "second\r\n"),
		},
		{
			name:  "sanitizes controls",
			input: []byte{'a', '\t', 0x1b, 0x7f, 0x80, 0xff, '\n'},
			want:  []byte(timestamp + "a^I^[^?~@~?\r\n"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatter := newConsoleLogFormatter()
			if got := formatter.format(tt.input, now); !bytes.Equal(got, tt.want) {
				t.Fatalf("format() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConsoleLogFormatterMaintainsLineState(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 34, 56, 0, time.UTC)
	timestamp := "2026-07-31 12:34:56 "
	formatter := newConsoleLogFormatter()

	var got []byte
	got = append(got, formatter.format([]byte("part"), now)...)
	got = append(got, formatter.format([]byte("ial\r"), now)...)
	got = append(got, formatter.format([]byte("\nnext\n"), now)...)

	want := []byte(timestamp + "partial\r\n" + timestamp + "next\r\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("split format() = %q, want %q", got, want)
	}
}

func TestBroadcastFormatsOnlyLogOutput(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "console.node")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := logFile.Close(); err != nil {
			t.Errorf("close log file: %v", err)
		}
	})

	clientCh := make(chan []byte, 1)
	console := &SSHConsole{
		clients: map[string]*consoleClient{
			"client": {ch: clientCh},
		},
		logFile:      logFile,
		reopenLogCh:  make(chan struct{}, 1),
		logFormatter: newConsoleLogFormatter(),
	}
	input := []byte("\x1b[31mred\r\n")
	console.broadcast(input)

	if got := <-clientCh; !bytes.Equal(got, input) {
		t.Fatalf("client output = %q, want raw output %q", got, input)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \^\[\[31mred\r\n$`)
	if !want.Match(logged) {
		t.Fatalf("log output = %q, want a timestamped and sanitized line", logged)
	}
}
