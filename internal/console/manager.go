package console

import (
	"fmt"
	"net/http"
	"log"
	"io"
	"os"
	"strconv"
	"context"
	"time"
	"sync"
	"os/exec"
	"errors"
	"syscall"
	
	"github.com/go-chi/chi/v5"
	"github.com/hpcloud/tail"
	"github.com/gorilla/websocket"
	"github.com/creack/pty"
)

type ConsoleService interface {
	doTailConsole(ctx context.Context, w http.ResponseWriter, r *http.Request) error
	doInteractConsole(ctx context.Context, w http.ResponseWriter, r *http.Request) error
}

type ConsoleManager struct {
	
}

// isEIO checks if an error is an I/O error (EIO)
// This happens when reading from a PTY after the process has been killed
func isEIO(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EIO
	}
	return false
}

// readFromPTYAndWriteToWebSocket reads from PTY and writes to WebSocket
func readFromPTYAndWriteToWebSocket(ptmx *os.File, conn *websocket.Conn, done <-chan struct{}, cleanup func(), wg *sync.WaitGroup) {
	defer wg.Done()
	
	buf := make([]byte, 4096)
	for {
		select {
		case <-done:
			return
		default:
			n, err := ptmx.Read(buf)
			if err != nil {
				// Don't log I/O errors - they're expected when the process is killed
				if err != io.EOF && !isEIO(err) {
					log.Printf("Error reading from PTY: %v", err)
				}
				cleanup()
				return
			}
			
			if n > 0 {
				if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					// Don't log if WebSocket is already closed (happens during normal shutdown)
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) &&
					   err.Error() != "websocket: close sent" {
						log.Printf("Failed to write to WebSocket: %v", err)
					}
					cleanup()
					return
				}
			}
		}
	}
}

// readFromWebSocketAndWriteToPTY reads from WebSocket and writes to PTY
func readFromWebSocketAndWriteToPTY(conn *websocket.Conn, ptmx *os.File, done <-chan struct{}, cleanup func(), wg *sync.WaitGroup, nodeID string) {
	defer wg.Done()
	
	for {
		select {
		case <-done:
			return
		default:
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				// Check if it's an unexpected close (not normal, going away, or abnormal)
				if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("WebSocket unexpected close error: %v", err)
				} else {
					log.Printf("WebSocket closed normally for console: %s", nodeID)
				}
				cleanup()
				return
			}
			
			if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
				// Write user input to PTY
				if _, err := ptmx.Write(message); err != nil {
					log.Printf("Failed to write to PTY: %v", err)
					cleanup()
					return
				}
			}
		}
	}
}

func drainAndCloseRequestBody(req *http.Request) {
	if req != nil && req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body) // ok even if already drained
		req.Body.Close()                     // ok even if already closed
	}
}


func (cs ConsoleManager) validateNode(id string) error {
	// make sure this is a valid node
	if _, ok := nodeCache[id]; !ok {
		log.Printf("%s is not a valid node.", id)
		msg := fmt.Sprintf("%s is not a valid node.", id)
		return newEndpointError(http.StatusNotFound, msg)
	}

	return nil
}

func (cs ConsoleManager) extractNodeId(w http.ResponseWriter, r *http.Request) (string, error) {
	nodeID := chi.URLParam(r, "nodeID")
	if nodeID == "" {
		log.Printf("There was an error reading the node ID from the request %s", r.URL.Path)
		msg := fmt.Sprintf("There was an error reading the node ID from the request %s", r.URL.Path)
		return "", newEndpointError(http.StatusBadRequest, msg)
	}

	return nodeID, nil
}

func (cs ConsoleManager) doTailConsole(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
// This is accessed with a connection that can be upgraded to a websocket.

	// Make sure the request is cleaned up
	defer drainAndCloseRequestBody(r)

	// Only allow 'GET' calls
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")

		return newEndpointError(http.StatusMethodNotAllowed,
			fmt.Sprintf("(%s) Not Allowed", r.Method))
	}

	nodeID, err := cs.extractNodeId(w, r)
	if err != nil {
		return err
	}

	// Make we are monitoring a valid node
	err = cs.validateNode(nodeID)
	if err != nil {
		return err
	}

	// Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("Error upgrading:", err)
		return newEndpointError(http.StatusInternalServerError,
			"Error upgrading connection")
	}
	defer conn.Close()

	// Channel to signal when to stop
	done := make(chan struct{})

	// Set up WebSocket ping/pong to keep connection alive
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Send periodic pings
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	params := r.URL.Query()

	follow := false
	if  followParam := params.Get("follow");  followParam != "" {
		var err error
		follow, err = strconv.ParseBool(followParam)
		if err != nil {
			return newEndpointError(http.StatusBadRequest,
				"Follow parameter must be a boolean value")		
		}
	}

	numLines := -1
	if numLinesParam := params.Get("lines"); numLinesParam != "" {
		numLines, err = strconv.Atoi(numLinesParam)
		if err != nil {
			return newEndpointError(http.StatusBadRequest,
				"Lines parameter must be a valid integer")		
		}
	}

	// Configuration for tail function -
	conf := tail.Config{
		Follow:    follow, 
		MustExist: false, // If file doesn't exist keep trying
		Poll:      true,  // Poll instead of using inotify -- inotify may not work on all filesystems
		Logger:    tail.DiscardingLogger,
	}

	// Only set ReOpen to true if we are following the file
	if follow {
		conf.ReOpen = true // If the files is deleted or moved, reopen original file
	}

	// If numLines is set to a positive number, we start reading from that many lines back
	if numLines > 0 {
		conf.Location = &tail.SeekInfo{Offset: int64(-1 * numLines), Whence: io.SeekEnd}
	}

	// TODO allow this prefix to be configurable, I think we can pass it to conmand
	filename := fmt.Sprintf("/var/log/conman/console.%s", nodeID)
	tail, err := tail.TailFile(filename, conf)
	if err != nil {
		log.Printf("Failed to tail file %s with error:%s", filename, err)
		return newEndpointError(http.StatusInternalServerError, "Failed to tail console")
	}

	defer close(done)

	// Read the lines of the tail output while looking for a cancel signal
	for {
		select {
		case <-ctx.Done():
			// done tailing this file - exit
			log.Printf("Tailing console for '%s' exiting", nodeID)

			// Received signal to stop so exit, unless this is shut down correctly, it will crash when
			// the next poll interval hits after this removal.
			tail.Config.Poll = false
			tail.Cleanup()
			tail.Stop()
		case line := <-tail.Lines:
			// Stream the line to the websocket
			if line == nil {
				// Reached end of file
				if !follow {
					// If we are not following the file, exit
					log.Printf("Tailing console for '%s' reached end of file, exiting", nodeID)

					tail.Config.Poll = false
					tail.Cleanup()
					tail.Stop()
					return nil
				}
				// If we are following the file, just wait for more data
				continue
			}

			// Add newline back (tail library strips it)
			lineText := line.Text + "\n"
			err := conn.WriteMessage(websocket.TextMessage, []byte(lineText))
			if err != nil {
				log.Printf("Failed to write message to websocket: %s", err)
				
				tail.Config.Poll = false
				tail.Cleanup()
				tail.Stop()
				return newEndpointError(http.StatusInternalServerError, "Failed to write to websocket")
			}
			
		}
	}
}

func (cs ConsoleManager) doInteractConsole(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	// Make sure the request is cleaned up
	defer drainAndCloseRequestBody(r)

	// Only allow 'GET' calls
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")

		return newEndpointError(http.StatusMethodNotAllowed,
			fmt.Sprintf("(%s) Not Allowed", r.Method))
	}

	nodeID, err := cs.extractNodeId(w, r)
	if err != nil {
		return err
	}

	// Make we are monitoring a valid node
	err = cs.validateNode(nodeID)
	if err != nil {
		return err
	}

	// Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade WebSocket connection: %v", err)
		return newEndpointError(http.StatusInternalServerError,
			"Error upgrading connection")
	}
	defer conn.Close()

	// Start conman process with PTY
	cmd := exec.Command("conman", nodeID)
	
	// Create a PTY for the command
	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("Failed to start conman with PTY: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: Failed to start conman with PTY"))
		conn.WriteMessage(websocket.CloseMessage,
            websocket.FormatCloseMessage(websocket.CloseInternalServerErr, ""))
		return nil
	}
	defer ptmx.Close()

	log.Printf("Started conman process for console: %s", nodeID)

	// Use a WaitGroup to coordinate goroutines
	var wg sync.WaitGroup
	
	// Channel to signal when to stop
	done := make(chan struct{})
	var doneOnce sync.Once
	
	// Track if process has been waited on
	processExited := make(chan struct{})
	var processExitedOnce sync.Once
	
	// Combined cleanup function that terminates process and signals done
	cleanup := func() {
		doneOnce.Do(func() {
			// Send ConMan's escape sequence to disconnect gracefully
			if ptmx != nil {
				log.Printf("Sending ConMan escape sequence (&.) to disconnect from console: %s", nodeID)
				ptmx.Write([]byte("&."))
				time.Sleep(100 * time.Millisecond) // Brief pause to let it process
			}
			close(done)
		})
	}

	// Goroutine to read from WebSocket and write to PTY
	wg.Add(1)
	go readFromWebSocketAndWriteToPTY(conn, ptmx, done, cleanup, &wg, nodeID)

	// Goroutine to read from PTY and write to WebSocket
	wg.Add(1)
	go readFromPTYAndWriteToWebSocket(ptmx, conn, done, cleanup, &wg)

	// Wait for the conman process to complete
	go func() {
		cmd.Wait()
		log.Printf("Conman process exited for console: %s", nodeID)
		processExitedOnce.Do(func() { close(processExited) })
	}()

	// Set up WebSocket ping/pong to keep connection alive
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Send periodic pings
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	// Wait for all goroutines to complete
	wg.Wait()
	
	// Give the process a moment to exit after cleanup was called
	log.Printf("Waiting for conman process to exit for console: %s", nodeID)
	select {
	case <-processExited:
		// Process exited gracefully
		log.Printf("Conman process exited gracefully for console: %s", nodeID)
	case <-time.After(2 * time.Second):
		// Process didn't exit in time, force kill
		log.Printf("Conman process didn't exit in time, forcing kill for console: %s", nodeID)
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		// Wait for the process to be reaped
		<-processExited
	}
	
	log.Printf("WebSocket connection closed for console: %s", nodeID)

	return nil
}


// NewConsoleManager factory function to create a new ConsoleService
func NewConsoleManager() ConsoleService {
	return &ConsoleManager{}
}
