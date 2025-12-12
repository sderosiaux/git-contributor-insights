package tui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Spinner provides animated feedback during long operations
type Spinner struct {
	frames   []string
	message  string
	writer   io.Writer
	stopChan chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	stopped  bool
}

// NewSpinner creates a new spinner with a message
func NewSpinner(writer io.Writer, message string) *Spinner {
	return &Spinner{
		frames:   []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		message:  message,
		writer:   writer,
		stopChan: make(chan struct{}),
	}
}

// Start begins the spinner animation
func (s *Spinner) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		frameIdx := 0

		for {
			select {
			case <-s.stopChan:
				// Clear the line
				fmt.Fprint(s.writer, "\r\033[K")
				return
			case <-ticker.C:
				// Update spinner frame
				frame := s.frames[frameIdx%len(s.frames)]
				cyan := "\033[36m"
				reset := "\033[0m"
				fmt.Fprintf(s.writer, "\r%s%s%s %s", cyan, frame, reset, s.message)
				frameIdx++
			}
		}
	}()
}

// Stop halts the spinner and clears the line
func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return
	}

	s.stopped = true
	close(s.stopChan)
	s.wg.Wait()
}

// UpdateMessage updates the spinner message
func (s *Spinner) UpdateMessage(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.message = message
}
