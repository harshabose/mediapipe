package duplexers

//
// type LocalUDP struct {
// 	bind   *net.UDPConn // UDP socket for bidirectional communication
// 	remote *net.UDPAddr // Remote endpoint address (auto-discovered or manually set)
//
// 	metrics *metrics.UnifiedMetrics // Real-time performance metrics
//
// 	mux sync.RWMutex // Protects concurrent access to connection state
// }
//
// func NewLoopBack(ctx context.Context, addr string, options ...LoopBackOption) (*LocalUDP, error) {
//
// 	addr, err := net.ResolveUDPAddr("udp", addr)
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to resolve bind address: %v", err)
// 	}
//
// 	conn, err := net.ListenUDP("udp", addr)
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to bind UDP socket: %v", err)
// 	}
//
// 	l := &LocalUDP{
// 		bind:    conn,
// 		metrics: metrics.NewUnifiedMetrics(ctx, fmt.Sprintf("LocalUDP-%s", addr.String()), 5, 3*time.Second),
// 	}
//
// 	l.metrics.SetState(metrics.ConnectingState)
//
// 	for _, option := range options {
// 		if err := option(l); err != nil {
// 			return nil, fmt.Errorf("error while applying option; err: %v", err)
// 		}
// 	}
//
// 	l.metrics.SetState(metrics.ConnectedState)
//
// 	fmt.Printf("LocalUDP connected on %s\n", l.bind.LocalAddr().String())
//
// 	return l, nil
// }
//
// func (l *LocalUDP) Consume(payload []byte) error {
// 	return l.write(payload)
// }
//
// func (l *LocalUDP) write(payload []byte) error {
// 	l.mux.RLock()
// 	defer l.mux.RUnlock()
//
// 	if l.bind == nil {
// 		err := fmt.Errorf("bind port not yet set")
// 		l.metrics.AddErrors(err)
// 		l.metrics.SetState(metrics.ErrorState)
// 		fmt.Printf("bind port not yet set. Skipping message; no error")
// 		return nil
// 	}
// 	if l.remote == nil {
// 		fmt.Printf("remote port not yet discovered. Skipping message; no error")
// 		return nil
// 	}
//
// 	bytesWritten, err := l.bind.WriteToUDP(payload, l.remote)
// 	if err != nil {
// 		l.metrics.AddErrors(err)
// 		l.metrics.SetState(metrics.ErrorState)
// 		return fmt.Errorf("failed to write UDP message: %v", err)
// 	}
//
// 	if bytesWritten != len(payload) {
// 		err := fmt.Errorf("written bytes (%d) != message length (%d)", bytesWritten, len(payload))
// 		l.metrics.AddErrors(err)
// 		return err
// 	}
//
// 	// Update metrics
// 	l.metrics.IncrementBytesWritten(uint64(bytesWritten))
// 	l.metrics.IncrementPacketsWritten()
// 	l.metrics.SetLastWriteTime(time.Now())
//
// 	if l.metrics.GetState() != metrics.ConnectedState {
// 		l.metrics.SetState(metrics.ConnectedState)
// 	}
//
// 	return nil
// }
//
// func (l *LocalUDP) Generate() ([]byte, error) {
// 	data, err := l.read()
// 	if err != nil {
// 		l.metrics.AddErrors(err)
// 		return nil, fmt.Errorf("ERROR: udp failed to generate; err: %w", err)
// 	}
//
// 	return data, nil
// }
//
// func (l *LocalUDP) read() ([]byte, error) {
// 	// Set read timeout to allow periodic context checking // TODO: ADD CONSISTENT TIMEOUT
// 	if err := l.bind.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
// 		l.metrics.AddErrors(err)
// 		fmt.Printf("Error while setting read deadline on UDP bind port: %v. Continuing...\n", err)
// 	}
//
// 	buff, nRead := l.readMessageFromUDPPort()
// 	if nRead > 0 && buff != nil {
// 		// Update metrics
// 		l.metrics.IncrementBytesRead(uint64(nRead))
// 		l.metrics.IncrementPacketsRead()
// 		l.metrics.SetLastReadTime(time.Now())
//
// 		if l.metrics.GetState() != metrics.ConnectedState {
// 			l.metrics.SetState(metrics.ConnectedState)
// 		}
//
// 		return buff[:nRead], nil
// 	}
//
// 	// Return nil to indicate no data available (Reader should handle appropriately)
// 	return nil, nil
// }
//
// func (l *LocalUDP) Close() error {
// 	l.mux.Lock()
// 	defer l.mux.Unlock()
//
// 	fmt.Println("Closing LocalUDP...")
//
// 	l.metrics.SetState(metrics.DisconnectedState)
//
// 	var err error
// 	if l.bind != nil {
// 		err = l.bind.Close()
// 		l.bind = nil
// 	}
//
// 	if l.metrics != nil {
// 		if metricsErr := l.metrics.Close(); metricsErr != nil {
// 			fmt.Printf("Error closing metrics: %v\n", metricsErr)
// 		}
// 	}
//
// 	fmt.Println("LocalUDP closed")
// 	return err
// }
//
// func (l *LocalUDP) readMessageFromUDPPort() ([]byte, int) {
// 	buffer := make([]byte, 1500) // Standard MTU size
//
// 	nRead, senderAddr, err := l.bind.ReadFromUDP(buffer)
// 	if err != nil {
// 		var netErr net.Error
// 		if errors.As(err, &netErr) && netErr.Timeout() {
// 			// Timeout is not really an error
// 			return nil, 0
// 		}
// 		l.metrics.AddErrors(err)
// 		l.metrics.SetState(metrics.ErrorState)
// 		fmt.Printf("Error while reading message from bind port: %v\n", err)
// 		return nil, 0
// 	}
//
// 	// Auto-discover remote address from first received packet
// 	l.mux.Lock()
// 	if l.remote == nil {
// 		l.remote = &net.UDPAddr{IP: senderAddr.IP, Port: senderAddr.Port}
// 		fmt.Printf("Auto-discovered remote address: %s\n", l.remote.String())
// 	}
// 	l.mux.Unlock()
//
// 	if senderAddr != nil && l.remote != nil && senderAddr.Port != l.remote.Port {
// 		err := fmt.Errorf("expected port %d but got %d", l.remote.Port, senderAddr.Port)
// 		l.metrics.AddErrors(err)
// 		fmt.Printf("Warning: %v\n", err)
// 	}
//
// 	return buffer, nRead
// }
//
// func (l *LocalUDP) SetRemoteAddress(address string) error {
// 	addr, err := net.ResolveUDPAddr("udp", address)
// 	if err != nil {
// 		l.metrics.AddErrors(err)
// 		return fmt.Errorf("failed to resolve remote address: %v", err)
// 	}
//
// 	l.mux.Lock()
// 	defer l.mux.Unlock()
// 	l.remote = addr
//
// 	fmt.Printf("Remote address set to: %s\n", addr.String())
// 	return nil
// }
//
// func (l *LocalUDP) GetMetrics() metrics.Snapshot {
// 	return l.metrics.GetSnapshot()
// }
//
// func (l *LocalUDP) GetLocalAddress() string {
// 	l.mux.RLock()
// 	defer l.mux.RUnlock()
//
// 	if l.bind == nil {
// 		return ""
// 	}
// 	return l.bind.LocalAddr().String()
// }
//
// func (l *LocalUDP) GetRemoteAddress() string {
// 	l.mux.RLock()
// 	defer l.mux.RUnlock()
//
// 	if l.remote == nil {
// 		return ""
// 	}
// 	return l.remote.String()
// }
