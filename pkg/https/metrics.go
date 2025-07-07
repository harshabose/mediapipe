package https

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
)

type ServerState string

const (
	ServerDown ServerState = "SERVER_OFFLINE"
	ServerUp   ServerState = "SERVER_ONLINE"
)

func isLoopBack(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return strings.ToLower(host) == "localhost"
	}

	return ip.IsLoopback()
}

// BufferedErrors is a fixed-size buffer for storing error messages
// It will remove the oldest error when the buffer is full
type BufferedErrors struct {
	maxSize int
	errors  []string
	mux     sync.RWMutex
}

// NewBufferedErrors creates a new BufferedErrors with the specified maximum size
func NewBufferedErrors(maxSize int) *BufferedErrors {
	return &BufferedErrors{
		maxSize: maxSize,
		errors:  make([]string, 0, maxSize),
	}
}

// Add adds a new error to the buffer, removing the oldest one if the buffer is full
func (be *BufferedErrors) Add(err string) {
	be.mux.Lock()
	defer be.mux.Unlock()

	if len(be.errors) >= be.maxSize {
		// Remove the oldest error (first element)
		be.errors = be.errors[1:]
	}

	// Add the new error
	be.errors = append(be.errors, err)
}

// MarshalJSON implements the json.Marshaler interface
func (be *BufferedErrors) MarshalJSON() ([]byte, error) {
	be.mux.RLock()
	defer be.mux.RUnlock()

	return json.Marshal(be.errors)
}

type health struct {
	State        ServerState     `json:"state"`
	RecentErrors *BufferedErrors `json:"recent_errors"`
	mux          sync.RWMutex
}

func (h *health) SetState(state ServerState) {
	h.mux.Lock()
	defer h.mux.Unlock()

	h.State = state
}

func (h *health) Marshal() ([]byte, error) {
	h.mux.RLock()
	defer h.mux.RUnlock()

	return json.Marshal(h)
}

// AddError adds an error message to the RecentErrors buffer
func (h *health) AddError(err string) {
	h.mux.Lock()
	defer h.mux.Unlock()

	if h.RecentErrors != nil {
		h.RecentErrors.Add(err)
	}
}
