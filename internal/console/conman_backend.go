// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/OpenCHAMI/remote-console/internal/nodes"
	"github.com/creack/pty"
)

const (
	conmanOutputBufferDepth = 256
	conmanReadBufferSize    = 4096
	conmanReconnectMin      = time.Second
	conmanReconnectMax      = 10 * time.Second
	conmanStableConnection  = 30 * time.Second
	conmanExitTimeout       = 100 * time.Millisecond
	conmanTerminateTimeout  = time.Second
)

type conmanProcess struct {
	nodeID string
	cmd    *exec.Cmd
	ptmx   *os.File
	done   chan struct{}

	stopOnce sync.Once
}

func launchConmanProcess(nodeID string) (*conmanProcess, error) {
	return launchPTYProcess(nodeID, exec.Command("conman", nodeID))
}

func launchPTYProcess(nodeID string, cmd *exec.Cmd) (*conmanProcess, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start conman PTY: %w", err)
	}

	process := &conmanProcess{
		nodeID: nodeID,
		cmd:    cmd,
		ptmx:   ptmx,
		done:   make(chan struct{}),
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Debug("ConMan client exited", "nodeID", nodeID, "error", err)
		}
		close(process.done)
	}()

	return process, nil
}

func (p *conmanProcess) stop() {
	p.stopOnce.Do(func() {
		if p.ptmx != nil {
			escapeDone := make(chan struct{})

			// Send escape sequence to close the PTY
			go func() {
				_, _ = p.ptmx.Write([]byte("&."))
				close(escapeDone)
			}()
			_ = waitForProcess(escapeDone, conmanExitTimeout)
		}

		// Wait for the process to finish
		if waitForProcess(p.done, conmanExitTimeout) {
			if p.ptmx != nil {
				_ = p.ptmx.Close()
			}
			return
		}

		// If the process is still running, send SIGTERM
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
		if p.ptmx != nil {
			_ = p.ptmx.Close()
		}

		if waitForProcess(p.done, conmanTerminateTimeout) {
			return
		}

		// If the process is still running, send SIGKILL
		if p.cmd != nil && p.cmd.Process != nil {
			slog.Warn("ConMan client did not stop after SIGTERM, killing it", "nodeID", p.nodeID)
			_ = p.cmd.Process.Kill()
			_ = waitForProcess(p.done, conmanTerminateTimeout)
		}
	})
}

func waitForProcess(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// conmanConsoleIO implements consoleIO for IPMI nodes managed by ConMan.
// It keeps a ConMan client process alive while the node remains eligible.
type conmanConsoleIO struct {
	nodeID string

	mu      sync.Mutex
	process *conmanProcess

	out     chan []byte
	dropped int
	ctx     context.Context
	cancel  context.CancelFunc

	closeOnce sync.Once
}

// newConmanConsoleIO creates and starts a ConMan backend for an IPMI node.
func newConmanConsoleIO(nodeID string) (*conmanConsoleIO, error) {
	if nodes.CurrentNode(nodeID) == nil {
		return nil, fmt.Errorf("node %s is not current", nodeID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &conmanConsoleIO{
		nodeID: nodeID,
		out:    make(chan []byte, conmanOutputBufferDepth),
		ctx:    ctx,
		cancel: cancel,
	}

	go c.run()
	return c, nil
}

// run is the internal goroutine that manages the conman process lifecycle.
func (c *conmanConsoleIO) run() {
	defer close(c.out)

	backoff := conmanReconnectMin
	for {
		if c.ctx.Err() != nil {
			return
		}

		// Check if the node is still current
		if !c.nodeIsCurrent() {
			slog.Info("Node is no longer managed by ConMan", "nodeID", c.nodeID)
			return
		}

		// Start the process
		started := time.Now()
		process, err := c.startProcess()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}

			slog.Error("Failed to start conman process", "nodeID", c.nodeID, "error", err)
			message := fmt.Sprintf("\n[Unable to start ConMan for %s. Retrying...]\n", c.nodeID)
			if !c.queueOutput([]byte(message)) || !c.waitToReconnect(backoff) {
				return
			}
			backoff = nextConmanBackoff(backoff)
			continue
		}

		// Read until the PTY closes.
		c.streamOutput(process)
		// Remove the process from the backend.
		c.clearProcess(process)
		// Stop and reap the process.
		process.stop()

		if c.ctx.Err() != nil {
			return
		}
		if !c.nodeIsCurrent() {
			slog.Info("Node is no longer managed by ConMan", "nodeID", c.nodeID)
			return
		}

		// Reset the backoff after a stable connection or increase it after an early exit.
		delay := backoff
		if time.Since(started) >= conmanStableConnection {
			backoff = conmanReconnectMin
			delay = backoff
		} else {
			backoff = nextConmanBackoff(backoff)
		}

		message := fmt.Sprintf("\n[Reconnecting to %s...]\n", c.nodeID)
		if !c.queueOutput([]byte(message)) || !c.waitToReconnect(delay) {
			return
		}
	}
}

func (c *conmanConsoleIO) startProcess() (*conmanProcess, error) {
	if c.ctx.Err() != nil {
		return nil, context.Canceled
	}

	process, err := launchConmanProcess(c.nodeID)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		process.stop()
		return nil, context.Canceled
	}
	c.process = process
	c.mu.Unlock()

	return process, nil
}

func (c *conmanConsoleIO) clearProcess(process *conmanProcess) {
	c.mu.Lock()
	if c.process == process {
		c.process = nil
	}
	c.mu.Unlock()
}

func (c *conmanConsoleIO) currentProcess() *conmanProcess {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.process
}

func (c *conmanConsoleIO) nodeIsCurrent() bool {
	return nodes.CurrentNode(c.nodeID) != nil
}

func (c *conmanConsoleIO) waitToReconnect(backoff time.Duration) bool {
	timer := time.NewTimer(jitterConmanBackoff(backoff))
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func nextConmanBackoff(current time.Duration) time.Duration {
	if current >= conmanReconnectMax/2 {
		return conmanReconnectMax
	}
	return current * 2
}

func jitterConmanBackoff(backoff time.Duration) time.Duration {
	if backoff <= 0 {
		return 0
	}
	return backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1))
}

func (c *conmanConsoleIO) streamOutput(process *conmanProcess) {
	buf := make([]byte, conmanReadBufferSize)
	for {
		n, err := process.ptmx.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if !c.queueOutput(data) {
				return
			}
		}
		if err != nil {
			// EOF, EIO, and a closed PTY are expected during shutdown.
			if c.ctx.Err() == nil &&
				!errors.Is(err, io.EOF) &&
				!errors.Is(err, os.ErrClosed) &&
				!errors.Is(err, syscall.EIO) {
				slog.Error("PTY read error", "nodeID", c.nodeID, "error", err)
			}
			return
		}
	}
}

func (c *conmanConsoleIO) queueOutput(data []byte) bool {
	if c.dropped > 0 {
		notice := fmt.Appendf(nil, "\n[Console %s: %d bytes of output dropped, client not keeping up]\n",
			c.nodeID, c.dropped)
		select {
		case c.out <- notice:
			slog.Info("ConMan console client caught up after dropping output",
				"nodeID", c.nodeID, "bytesDropped", c.dropped)
			c.dropped = 0
		case <-c.ctx.Done():
			return false
		default:
			c.dropped += len(data)
			return true
		}
	}

	select {
	case c.out <- data:
		return true
	case <-c.ctx.Done():
		return false
	default:
		if c.dropped == 0 {
			slog.Warn("ConMan console client not keeping up, dropping output", "nodeID", c.nodeID)
		}
		c.dropped += len(data)
		return true
	}
}

func (c *conmanConsoleIO) Output() <-chan []byte { return c.out }

func (c *conmanConsoleIO) Write(p []byte) (int, error) {
	process := c.currentProcess()
	if process == nil {
		return 0, errNotConnected
	}

	n, err := process.ptmx.Write(p)
	if err != nil {
		return n, fmt.Errorf("%w: %w", errNotConnected, err)
	}

	return n, nil
}

func (c *conmanConsoleIO) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()

		c.mu.Lock()
		process := c.process
		c.process = nil
		c.mu.Unlock()

		if process != nil {
			process.stop()
		}
	})

	return nil
}
