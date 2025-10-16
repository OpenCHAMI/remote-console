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

// interactiveConsoleSession manages the lifecycle of an interactive console session
type interactiveConsoleSession struct {
	cmd          *exec.Cmd
	ptmx         *os.File
	conn         *websocket.Conn
	nodeID       string
	doneOnce     sync.Once
	processExit  chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
}

// cleanup performs graceful cleanup of the console session
func (s *interactiveConsoleSession) cleanup() {
	s.doneOnce.Do(func() {
		log.Printf("Starting cleanup for console session: %s", s.nodeID)
		
		// Cancel context to signal all goroutines
		if s.cancel != nil {
			s.cancel()
		}
		
		// Try graceful disconnect via ConMan escape sequence
		if s.ptmx != nil {
			log.Printf("Sending ConMan escape sequence (&.) to disconnect from console: %s", s.nodeID)
			s.ptmx.Write([]byte("&."))
			time.Sleep(100 * time.Millisecond) // Brief pause to let it process
		}
		
		// Ensure process termination
		if s.cmd != nil && s.cmd.Process != nil {
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-s.processExit:
				timer.Stop()
				log.Printf("Conman process exited gracefully for console: %s", s.nodeID)
			case <-timer.C:
				log.Printf("Force killing conman process for console: %s", s.nodeID)
				s.cmd.Process.Kill()
				// Wait for process to be reaped
				<-s.processExit
			}
		}

		// Close PTY
		if s.ptmx != nil {
			s.ptmx.Close()
		}
		
		// Close WebSocket connection gracefully
		if s.conn != nil {
			// Send close message first
			s.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			// Then close the connection
			s.conn.Close()
		}
		
		log.Printf("Cleanup completed for console session: %s", s.nodeID)
	})
}

// keepAlive manages WebSocket ping/pong to maintain connection for any session
func keepAlive(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		select {
		case <-ticker.C:
			fmt.Println("write")
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
			fmt.Println("after write")
		case <-ctx.Done():
			fmt.Println("done")
			return
		}
	}
}

// monitorProcess watches for process exit and triggers cleanup
func (s *interactiveConsoleSession) monitorProcess() {
	s.cmd.Wait()
	log.Printf("Conman process exited for console: %s", s.nodeID)
	close(s.processExit)
	s.cleanup()
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
func (s *interactiveConsoleSession) streamOutput(wg *sync.WaitGroup) {
	defer wg.Done()
	
	buf := make([]byte, 4096)
	for {
		select {
		case <-s.ctx.Done():
			s.cleanup()
			return
		default:
			n, err := s.ptmx.Read(buf)
			if err != nil {
				// Don't log I/O errors - they're expected when the process is killed
				if err != io.EOF && !isEIO(err) {
					log.Printf("Error reading from PTY: %v", err)
				}
				s.cleanup()
				return
			}
			
			if n > 0 {
				if err := s.conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					// Don't log if WebSocket is already closed (happens during normal shutdown)
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) &&
					   err.Error() != "websocket: close sent" {
						log.Printf("Failed to write to WebSocket: %v", err)
					}
					s.cleanup()
					return
				}
			}
		}
	}
}

// readFromWebSocketAndWriteToPTY reads from WebSocket and writes to PTY
func (s *interactiveConsoleSession) streamInput(wg *sync.WaitGroup) {
	defer wg.Done()
	
	for {
		select {
		case <-s.ctx.Done():
			s.cleanup()
			return
		default:
			messageType, message, err := s.conn.ReadMessage()
			if err != nil {
				// Check if it's an unexpected close (not normal, going away, or abnormal)
				if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("WebSocket unexpected close error: %v", err)
				} else {
					log.Printf("WebSocket closed normally for console: %s", s.nodeID)
				}
				s.cleanup()
				return
			}
			
			if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
				// Write user input to PTY
				if _, err := s.ptmx.Write(message); err != nil {
					log.Printf("Failed to write to PTY: %v", err)
					s.cleanup()
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

type consoleTailSession struct {
	nodeID string
	conn   *websocket.Conn
	ctx	context.Context
	cancel       context.CancelFunc
}

func newConsoleTailSession(ctx context.Context, nodeID string, conn *websocket.Conn) *consoleTailSession {
	sessionCtx, cancel := context.WithCancel(ctx)
	return &consoleTailSession{
		nodeID: nodeID,
		conn:   conn,
		ctx:	sessionCtx,
		cancel: cancel,
	}
}

func (cts *consoleTailSession) cleanup() {
	cts.cancel()
	if cts.conn != nil {
		// Send close message first
		cts.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		// Then close the connection
		cts.conn.Close()
	}
}

func (s *interactiveConsoleSession) keepAlive() {
	keepAlive(s.ctx, s.conn)
}

func (cts *consoleTailSession) keepAlive() {
	keepAlive(cts.ctx, cts.conn)
}

func (cts *consoleTailSession) waitForClose() {
	for {
		log.Printf("before")
		_, _, err := cts.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket closed normally for tail session '%s'", cts.nodeID)
			} else {
				log.Printf("WebSocket closed for tail session '%s', error: %v", cts.nodeID, err)
			}

			cts.cleanup()
			break
		}
	}
}

func (cts *consoleTailSession) streamConsoleTail(ctx context.Context, tail *tail.Tail, follow bool) {
// Read the lines of the tail output while looking for a cancel signal
       for {
	       select {
	       case <-ctx.Done():
		       // done tailing this file - exit
		       log.Printf("Tailing console for '%s' exiting", cts.nodeID)
		       tail.Config.Poll = false
		       tail.Cleanup()
		       tail.Stop()
		       return
	       case line := <-tail.Lines:
		       // Stream the line to the websocket
		       if line == nil {
			       // Reached end of file
			       if !follow {
				       // If we are not following the file, exit
				       log.Printf("Tailing console for '%s' reached end of file, exiting", cts.nodeID)

				       tail.Config.Poll = false
				       tail.Cleanup()
				       tail.Stop()
				       return
			       }
			       // If we are following the file, just wait for more data
			       continue
		       }

		       // Add newline back (tail library strips it)
		       lineText := line.Text + "\n"
		       err := cts.conn.WriteMessage(websocket.TextMessage, []byte(lineText))
		       if err != nil {
			       log.Printf("Failed to write message to websocket: %s", err)
                               
			       tail.Config.Poll = false
			       tail.Cleanup()
			       tail.Stop()
			       return
		       }
	       }
       }
}



func (cs ConsoleManager) doTailConsole(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	log.Printf("doTailConsole called")

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

	log.Printf("Starting console tail session for node: %s", nodeID)

	// Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("Error upgrading:", err)
		return newEndpointError(http.StatusInternalServerError,
			"Error upgrading connection")
	}

	session := newConsoleTailSession(ctx, nodeID, conn)
	if session == nil {
		// TODO not sure we can return the error this way as we gave upgraded the connection
		return newEndpointError(http.StatusInternalServerError,
			"Error starting console tail session")
	}

	defer session.cleanup() // Ensure cleanup always happens

	// Monitor WebSocket for closure by reading (and discarding) any messages
	// This is needed to detect when the client closes the connection
	// TODO this shouldn't be needed !!!! the done channel is being fired ....
	// go func() {
	// 	for {
	// 		_, _, err := session.conn.ReadMessage()
	// 		if err != nil {
	// 			log.Printf("WebSocket closed for tail session '%s', triggering cleanup", nodeID)
	// 			session.cleanup()
	// 			return
	// 		}
	// 		// Discard any messages received (tail is one-way: server -> client)
	// 	}
	// }()

	go session.keepAlive()

	params := r.URL.Query()

	follow := false
	if followParam := params.Get("follow"); followParam != "" {
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

	// Configuration for tail function
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

	log.Printf("Started tailing console log for: %s", nodeID)
	
	go session.waitForClose()

	// Start streaming the console output
	// TODO should this return an error?
	session.streamConsoleTail(ctx, tail, follow)
	
	return nil
}

func newInteractiveConsoleSession(ctx context.Context, nodeID string, conn *websocket.Conn) *interactiveConsoleSession {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &interactiveConsoleSession{
		nodeID:      nodeID,
		conn:        conn,
		processExit: make(chan struct{}),
		ctx:         sessionCtx,
		cancel:      cancel,
	}

		// Start conman process with PTY
	session.cmd = exec.Command("conman", nodeID)
	
	// Create a PTY for the command
	var err error
	session.ptmx, err = pty.Start(session.cmd)
	if err != nil {
		log.Printf("Failed to start conman with PTY: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: Failed to start conman with PTY"))
		return nil
	}
	
	return session
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
// conn.Close() is handled in session.cleanup()

	session := newInteractiveConsoleSession(ctx, nodeID, conn)
	if session == nil {

		// TODO not sure we can return the error this way as we gave upgraded the connection
		return newEndpointError(http.StatusInternalServerError,
			"Error starting console session")
	}

	defer session.cleanup() // Ensure cleanup always happens

	log.Printf("Started conman process for console: %s", nodeID)

	// Monitor process exit
	go session.monitorProcess()

	// Start keep-alive ping/pong
	go session.keepAlive()

	// // Handle context cancellation
	// go func() {
	// 	select {
	// 	case <-ctx.Done():
	// 		log.Printf("Context cancelled for console: %s", nodeID)
	// 		session.cleanup()
	// 	case <-session.done:
	// 		// Session already cleaning up
	// 	}
	// }()

	// Start I/O goroutines
	var wg sync.WaitGroup
	wg.Add(2)
	go session.streamInput(&wg)
	go session.streamOutput(&wg)

	// Wait for I/O goroutines to complete
	wg.Wait()
	
	log.Printf("Interactive console session ended for: %s", nodeID)
	return nil
}

// NewConsoleManager factory function to create a new ConsoleService
func NewConsoleManager() ConsoleService {
	return &ConsoleManager{}
}