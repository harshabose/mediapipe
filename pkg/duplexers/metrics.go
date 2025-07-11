package duplexers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type ClientState int

const (
	ClientStateDisconnected ClientState = iota
	ClientStateConnecting
	ClientStateConnected
	ClientStateError
)

func (cs ClientState) String() string {
	switch cs {
	case ClientStateDisconnected:
		return "Disconnected"
	case ClientStateConnecting:
		return "Connecting"
	case ClientStateConnected:
		return "Connected"
	case ClientStateError:
		return "Error"
	default:
		return "Unknown"
	}
}

type BufferedErrors struct {
	maxSize int
	errors  []ErrorEntry
	mux     sync.RWMutex
}

type ErrorEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

func NewBufferedErrors(maxSize int) *BufferedErrors {
	return &BufferedErrors{
		maxSize: maxSize,
		errors:  make([]ErrorEntry, 0, maxSize),
	}
}

func (be *BufferedErrors) Add(err error) {
	be.mux.Lock()
	defer be.mux.Unlock()

	if len(be.errors) >= be.maxSize {
		be.errors = be.errors[1:]
	}

	be.errors = append(be.errors, ErrorEntry{
		Timestamp: time.Now(),
		Message:   err.Error(),
	})
}

func (be *BufferedErrors) MarshalJSON() ([]byte, error) {
	be.mux.RLock()
	defer be.mux.RUnlock()

	return json.Marshal(be.errors)
}

type MetricsSnapshot struct {
	State            string          `json:"state"`
	PacketsRead      uint64          `json:"packetsRead"`
	PacketsWritten   uint64          `json:"packetsWritten"`
	BytesRead        uint64          `json:"bytesRead"`
	BytesWritten     uint64          `json:"bytesWritten"`
	BytesReadRate    float32         `json:"bytesReadRate"`
	BytesWrittenRate float32         `json:"bytesWrittenRate"`
	LastWriteTime    *time.Time      `json:"lastWriteTime,omitempty"`
	LastReadTime     *time.Time      `json:"lastReadTime,omitempty"`
	RecentErrors     *BufferedErrors `json:"recentErrors"`
}

type UnifiedMetrics struct {
	// Basic state
	state          ClientState
	packetsRead    uint64
	packetsWritten uint64
	bytesRead      uint64
	bytesWritten   uint64
	lastWriteTime  time.Time
	lastReadTime   time.Time
	recentErrors   *BufferedErrors

	// Rate calculation
	lastBytesRead    uint64
	lastBytesWritten uint64
	bytesReadRate    float32
	bytesWrittenRate float32
	tickerInterval   time.Duration

	// Control
	serviceTitle string
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mux          sync.RWMutex
}

func NewUnifiedMetrics(ctx context.Context, serviceTitle string, maxErrorBuffer int, tickerInterval time.Duration) *UnifiedMetrics {
	ctx2, cancel := context.WithCancel(ctx)
	m := &UnifiedMetrics{
		state:          ClientStateDisconnected,
		recentErrors:   NewBufferedErrors(maxErrorBuffer),
		serviceTitle:   serviceTitle,
		tickerInterval: tickerInterval,
		ctx:            ctx2,
		cancel:         cancel,
	}

	m.wg.Add(1)
	go m.loop()

	return m
}

func (m *UnifiedMetrics) loop() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.tickerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.updateRatesAndPrint()
		}
	}
}

func (m *UnifiedMetrics) updateRatesAndPrint() {
	m.mux.Lock()

	currentBytesRead := m.bytesRead
	currentBytesWritten := m.bytesWritten

	intervalSeconds := float32(m.tickerInterval.Seconds())
	m.bytesReadRate = float32(currentBytesRead-m.lastBytesRead) / intervalSeconds
	m.bytesWrittenRate = float32(currentBytesWritten-m.lastBytesWritten) / intervalSeconds

	m.lastBytesRead = currentBytesRead
	m.lastBytesWritten = currentBytesWritten

	m.mux.Unlock()

	snapshot := m.GetSnapshot()

	m.printFormattedMetrics(snapshot)
}

func (m *UnifiedMetrics) printFormattedMetrics(snapshot MetricsSnapshot) {
	divider := "================================================"

	fmt.Printf("\n%s\n", divider)
	fmt.Printf("%*s\n", (len(divider)+len(m.serviceTitle))/2, m.serviceTitle)
	fmt.Printf("%s\n\n", divider)

	fmt.Printf("%-20s: %s\n", "Connection State", snapshot.State)
	fmt.Println()

	fmt.Printf("%-20s: %d\n", "Packets Read", snapshot.PacketsRead)
	fmt.Printf("%-20s: %d\n", "Packets Written", snapshot.PacketsWritten)
	fmt.Println()

	fmt.Printf("%-20s: %s\n", "Bytes Read", formatBytes(snapshot.BytesRead))
	fmt.Printf("%-20s: %s\n", "Bytes Written", formatBytes(snapshot.BytesWritten))
	fmt.Println()

	fmt.Printf("%-20s: %s/s\n", "Read Rate", formatBytes(uint64(snapshot.BytesReadRate)))
	fmt.Printf("%-20s: %s/s\n", "Write Rate", formatBytes(uint64(snapshot.BytesWrittenRate)))
	fmt.Println()

	if snapshot.LastReadTime != nil {
		fmt.Printf("%-20s: %s\n", "Last Read", snapshot.LastReadTime.Format("2006-01-02 15:04:05"))
	}
	if snapshot.LastWriteTime != nil {
		fmt.Printf("%-20s: %s\n", "Last Write", snapshot.LastWriteTime.Format("2006-01-02 15:04:05"))
	}
	if snapshot.LastReadTime != nil || snapshot.LastWriteTime != nil {
		fmt.Println()
	}

	snapshot.RecentErrors.mux.RLock()
	errorCount := len(snapshot.RecentErrors.errors)
	if errorCount > 0 {
		fmt.Printf("Recent Errors (%d error", errorCount)
		if errorCount != 1 {
			fmt.Print("s")
		}
		fmt.Println("):")

		for _, errorEntry := range snapshot.RecentErrors.errors {
			fmt.Printf("            [%s] %s\n",
				errorEntry.Timestamp.Format("15:04:05"),
				errorEntry.Message)
		}
		fmt.Println()
	}
	snapshot.RecentErrors.mux.RUnlock()

	fmt.Printf("%s\n", divider)
}

func formatBytes(bytes uint64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.2f KB", float64(bytes)/1024)
	} else if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
	} else {
		return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
	}
}

func (m *UnifiedMetrics) Close() error {
	if m.cancel != nil {
		m.cancel()
		m.wg.Wait()
	}
	return nil
}

func (m *UnifiedMetrics) SetState(state ClientState) {
	m.mux.Lock()
	defer m.mux.Unlock()
	m.state = state
}

func (m *UnifiedMetrics) GetState() ClientState {
	m.mux.RLock()
	defer m.mux.RUnlock()
	return m.state
}

func (m *UnifiedMetrics) IncrementPacketsWritten() {
	m.mux.Lock()
	defer m.mux.Unlock()
	m.packetsWritten++
}

func (m *UnifiedMetrics) IncrementPacketsRead() {
	m.mux.Lock()
	defer m.mux.Unlock()
	m.packetsRead++
}

func (m *UnifiedMetrics) IncrementBytesWritten(bytes uint64) {
	m.mux.Lock()
	defer m.mux.Unlock()
	m.bytesWritten += bytes
}

func (m *UnifiedMetrics) IncrementBytesRead(bytes uint64) {
	m.mux.Lock()
	defer m.mux.Unlock()
	m.bytesRead += bytes
}

func (m *UnifiedMetrics) SetLastWriteTime(t time.Time) {
	m.mux.Lock()
	defer m.mux.Unlock()
	m.lastWriteTime = t
}

func (m *UnifiedMetrics) SetLastReadTime(t time.Time) {
	m.mux.Lock()
	defer m.mux.Unlock()
	m.lastReadTime = t
}

func (m *UnifiedMetrics) GetLastWriteTime() time.Time {
	m.mux.RLock()
	defer m.mux.RUnlock()
	return m.lastWriteTime
}

func (m *UnifiedMetrics) GetLastReadTime() time.Time {
	m.mux.RLock()
	defer m.mux.RUnlock()
	return m.lastReadTime
}

func (m *UnifiedMetrics) AddErrors(errs ...error) {
	if len(errs) == 0 {
		return
	}

	m.mux.Lock()
	defer m.mux.Unlock()

	for _, err := range errs {
		if err == nil {
			continue
		}
		m.recentErrors.Add(err)
	}
}

func (m *UnifiedMetrics) GetSnapshot() MetricsSnapshot {
	m.mux.RLock()
	defer m.mux.RUnlock()

	snapshot := MetricsSnapshot{
		State:            m.state.String(),
		PacketsRead:      m.packetsRead,
		PacketsWritten:   m.packetsWritten,
		BytesRead:        m.bytesRead,
		BytesWritten:     m.bytesWritten,
		BytesReadRate:    m.bytesReadRate,
		BytesWrittenRate: m.bytesWrittenRate,
		RecentErrors:     m.recentErrors,
	}

	if !m.lastReadTime.IsZero() {
		snapshot.LastReadTime = &m.lastReadTime
	}
	if !m.lastWriteTime.IsZero() {
		snapshot.LastWriteTime = &m.lastWriteTime
	}

	return snapshot
}
