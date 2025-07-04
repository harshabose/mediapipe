// Package mediapipe provides a universal, type-safe streaming media pipeline with
// library-agnostic data transformation capabilities.
//
// # Core Concept: Universal Media Router
//
// This package implements a universal media routing system that treats all data types
// (video, audio, telemetry, raw data) uniformly while allowing seamless transformation
// for different output protocols and libraries. The key innovation is the separation
// of data flow from data transformation, enabling the same data stream to feed multiple
// consumers with different interface requirements.
//
// # The Data Transformation Pattern
//
// The package revolves around the Data[D, T] struct, which represents data that can be
// lazily transformed into any target type D when needed. This pattern solves the common
// problem in media streaming where:
//
//   - Same RTP video stream needs to go to: RTSP server (*rtp.Packet), UDP socket ([]byte), WebRTC (custom format)
//   - Same MAVLINK telemetry needs to go to: Mission Planner (WebSocket frames), qGroundControl (TCP bytes), dashboard (JSON)
//   - Same audio stream needs to go to: RTSP server, SFU, local recording, live website
//
// # Architecture Flow
//
//   Raw Data Source → AnyReader (wraps as Data[D, T]) → BufferedReader (async buffering) → Multiple Consumers
//
// Each consumer can transform the same buffered Data element to their required format
// using the Get() method, with transformations happening only when needed (lazy evaluation).
//
// # Type Safety and Performance
//
// The generic type system ensures compile-time safety:
//   - D: The target type that Data can be transformed into
//   - T: The source type from the original data generator
//
// Performance benefits:
//   - No upfront data conversion (lazy transformation)
//   - Single data pipeline serves multiple output formats
//   - Memory efficient buffering of source data
//   - Zero-copy transformations where possible
//
// # Example Usage
//
//   // 1. WebRTC video to multiple outputs
//   rtcTrack := &WebRTCTrackSource{}
//
//   // Create readers for different target formats
//   rtspReader := NewReader(rtcTrack, func(p *rtp.Packet) (*rtp.Packet, error) { return p, nil })
//   udpReader := NewReader(rtcTrack, func(p *rtp.Packet) ([]byte, error) { return p.Marshal() })
//
//   // Buffer both readers
//   rtspBuffered := NewBufferedReader(ctx, rtspReader, 100)
//   udpBuffered := NewBufferedReader(ctx, udpReader, 100)
//
//   // Consumers read and transform as needed
//   rtspData, _ := rtspBuffered.Generate()
//   packet, _ := rtspData.Get() // Gets *rtp.Packet for RTSP server
//
//   udpData, _ := udpBuffered.Generate()
//   bytes, _ := udpData.Get()    // Gets []byte for UDP transmission
//
//   // 2. MAVLINK telemetry to multiple ground stations
//   mavlinkConn := &MAVLinkSource{}
//
//   wsReader := NewReader(mavlinkConn, func(data []byte) ([]byte, error) {
//       return addWebSocketFraming(data)
//   })
//   tcpReader := NewReader(mavlinkConn, func(data []byte) ([]byte, error) {
//       return addTCPHeaders(data)
//   })

package mediapipe

// Data wraps source data of type T with a transformation function that can convert
// it to target type D. This is the core abstraction of the universal media routing
// system, serving as the bridge between raw data sources and the transformation system.
//
// Type parameters:
//   - D: The target type that this Data can be transformed into
//   - T: The source type of the raw data being wrapped
//
// The Data struct uses function composition to defer transformation until Get() is called,
// enabling lazy evaluation and memory efficiency in the buffering system.
//
// Example transformations:
//   - RTP packet Data → *rtp.Packet for gortsplib (RTSP server)
//   - RTP packet Data → []byte for UDP transmission
//   - MAVLINK Data → []byte with WebSocket framing for Mission Planner
//   - MAVLINK Data → []byte with TCP headers for qGroundControl
//   - Audio Data → PCM samples for recording, compressed stream for transmission
type Data[D, T any] struct {
	data        T                  // The source data to be transformed
	transformer func(T) (D, error) // Function that transforms T into D
}

// Wrap creates a new Data that can transform source data T into target type D
// using the provided transformer function.
//
// This function is the primary factory for creating Data[D, T] elements in the system.
// It encapsulates the source data with its transformation logic, creating a self-contained
// unit that can be buffered, passed around, and transformed on-demand.
//
// Parameters:
//   - data: The source data of type T to be wrapped
//   - transformer: Function that converts T to D. Must not be nil.
//
// The transformer function should be pure (no side effects) and deterministic
// for consistent behavior across multiple Get() calls on the same Data.
//
// Example:
//
//	// Wrap RTP packet for RTSP output
//	data := Wrap(rtpPacket, func(p *rtp.Packet) (*rtp.Packet, error) {
//	    return p, nil  // Identity transformation
//	})
//
//	// Wrap RTP packet for UDP output
//	data := Wrap(rtpPacket, func(p *rtp.Packet) ([]byte, error) {
//	    return p.Marshal()  // Transform to bytes
//	})
func Wrap[D, T any](data T, transformer func(T) (D, error)) *Data[D, T] {
	if transformer == nil {
		panic("transformer cannot be nil - this should not happen")
	}
	return &Data[D, T]{
		data:        data,
		transformer: transformer,
	}
}

// Get applies the transformation function to convert the source data T into the target type D.
//
// This method performs the actual transformation that was deferred during Data
// creation. It can be called multiple times and should return consistent results
// for the same source data (assuming the transformer function is deterministic).
//
// The transformation is performed lazily - only when actually needed by a consumer.
// This can also be used for any modification or processing of the data, allowing
// multiple consumers to transform the same source data differently without wasting
// memory or CPU on unused transformations.
//
// Returns the transformed data of type D, or an error if transformation fails
// (e.g., marshaling errors, invalid data format, or transformation-specific failures).
func (d *Data[D, T]) Get() (D, error) {
	return d.transformer(d.data)
}
