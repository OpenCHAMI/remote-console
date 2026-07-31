// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package console

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConmanConsoleIOCloseIsIdempotent(t *testing.T) {
	var cancelCalls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	c := &conmanConsoleIO{
		ctx: ctx,
		cancel: func() {
			cancelCalls.Add(1)
			cancel()
		},
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := c.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
	wg.Wait()

	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("cancel called %d times, want 1", got)
	}

	if _, err := c.startProcess(); !errors.Is(err, context.Canceled) {
		t.Fatalf("startProcess after Close returned %v, want context.Canceled", err)
	}
}

func TestConmanProcessStopReapsChild(t *testing.T) {
	process, err := launchPTYProcess("test", exec.Command("cat"))
	if err != nil {
		t.Fatal(err)
	}

	process.stop()
	process.stop()

	select {
	case <-process.done:
	case <-time.After(2 * time.Second):
		t.Fatal("conman process was not reaped after shutdown")
	}
}

func TestConmanConsoleIOReadWrite(t *testing.T) {
	process, err := launchPTYProcess("test", exec.Command("cat"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &conmanConsoleIO{
		nodeID:  "test",
		process: process,
		out:     make(chan []byte, conmanOutputBufferDepth),
		ctx:     ctx,
		cancel:  cancel,
	}
	t.Cleanup(func() { _ = c.Close() })

	readDone := make(chan struct{})
	go func() {
		c.streamOutput(process)
		close(readDone)
	}()

	input := []byte("hello\n")
	if _, err := c.Write(input); err != nil {
		t.Fatalf("Write: %v", err)
	}

	select {
	case output := <-c.Output():
		if !bytes.Contains(output, []byte("hello")) {
			t.Fatalf("output %q does not contain input", output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for PTY output")
	}

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("PTY read did not stop after Close")
	}
}

func TestConmanConsoleIOWriteWhileDisconnected(t *testing.T) {
	c := &conmanConsoleIO{}
	if _, err := c.Write([]byte("input")); !errors.Is(err, errNotConnected) {
		t.Fatalf("Write returned %v, want errNotConnected", err)
	}
}

func TestConmanConsoleIOCloseUnblocksWriter(t *testing.T) {
	process, err := launchPTYProcess("test", exec.Command("sleep", "30"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &conmanConsoleIO{
		nodeID:  "test",
		process: process,
		out:     make(chan []byte, conmanOutputBufferDepth),
		ctx:     ctx,
		cancel:  cancel,
	}

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		input := make([]byte, maxWebSocketMessageSize)
		for {
			if _, err := c.Write(input); err != nil {
				return
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock a PTY writer")
	}
}

func TestConmanConsoleIOReportsDroppedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &conmanConsoleIO{
		nodeID: "test",
		out:    make(chan []byte, 1),
		ctx:    ctx,
	}

	if !c.queueOutput([]byte("first")) || !c.queueOutput([]byte("dropped")) {
		t.Fatal("queueOutput stopped while the context was active")
	}
	<-c.out

	if !c.queueOutput([]byte("next")) {
		t.Fatal("queueOutput stopped while the context was active")
	}
	select {
	case output := <-c.out:
		if !bytes.Contains(output, []byte("7 bytes of output dropped")) {
			t.Fatalf("output %q does not report the dropped bytes", output)
		}
	default:
		t.Fatal("missing dropped output notice")
	}
}

func TestConmanReconnectBackoff(t *testing.T) {
	if got := nextConmanBackoff(time.Second); got != 2*time.Second {
		t.Fatalf("next backoff = %s, want 2s", got)
	}
	if got := nextConmanBackoff(20 * time.Second); got != conmanReconnectMax {
		t.Fatalf("capped backoff = %s, want %s", got, conmanReconnectMax)
	}

	for range 100 {
		got := jitterConmanBackoff(10 * time.Second)
		if got < 5*time.Second || got > 10*time.Second {
			t.Fatalf("jittered backoff %s is outside [5s, 10s]", got)
		}
	}
}
