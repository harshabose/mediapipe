package sample

import (
	"github.com/pion/webrtc/v4/pkg/media"
)

// CanConsumePionSamplePacket defines the interface for Pion WebRTC objects that can consume media samples.
// This interface matches the signature used by Pion's TrackLocalStaticSample.WriteSample() method,
// which accepts media.Sample objects and handles all the underlying RTP packetization automatically.
//
// The media.Sample contains the raw media data along with timing information:
//   - Data: The actual media payload (audio/video frames)
//   - Duration: Timing information for proper playback
//   - Timestamp: Media-specific timestamp for synchronization
//
// This interface abstracts away the complexity of RTP packet creation, sequencing,
// and timing management that would otherwise need to be handled manually when
// working with TrackLocalStaticRTP directly.
//
// This interface is specifically designed for Pion WebRTC TrackLocalStaticSample objects
// and similar Pion components that follow this method signature pattern for simplified
// media sample writing.
type CanConsumePionSamplePacket interface {
	// WriteSample accepts a media sample and handles all RTP packetization internally.
	// This matches the exact signature of Pion WebRTC's TrackLocalStaticSample.WriteSample() method.
	//
	// The method automatically handles:
	//   - RTP packet creation and sequencing
	//   - Timestamp management and clock rate conversion
	//   - Payload-specific packetization (H.264, Opus, etc.)
	//   - Network transmission to connected peers
	//
	// Parameters:
	//   - media.Sample: The media sample containing data, duration, and timestamp
	//
	// Returns:
	//   - error: Any error that occurred during sample processing or transmission
	WriteSample(media.Sample) error
}

// PionSampleConsumer is an adapter that converts a CanConsumePionSamplePacket into a CanConsume[media.Sample].
// This adapter allows Pion WebRTC objects (like TrackLocalStaticSample) to be used with the universal
// AnyWriter system, enabling seamless integration with the media routing pipeline.
//
// This is the most common adaptation needed when outputting to Pion WebRTC tracks, as most streaming
// applications want to send media samples without manually handling RTP packet creation, sequencing,
// and timing management. The adapter provides a clean integration point between the universal
// media routing system and Pion's simplified sample-based API.
//
// The PionSampleConsumer is particularly useful when you want Pion to handle all the RTP complexity
// automatically, including codec-specific packetization, proper timestamp management, and sequence
// number generation.
//
// Example usage with Pion WebRTC:
//
//	trackLocal := // ... TrackLocalStaticSample obtained from Pion WebRTC
//	pionConsumer := NewPionSampleConsumer(trackLocal)
//	writer := NewIdentityAnyWriter(pionConsumer)
//	buffered := NewBufferedWriter(ctx, writer, 100)
type PionSampleConsumer struct {
	consumer CanConsumePionSamplePacket // The Pion WebRTC object (e.g., TrackLocalStaticSample)
}

// NewPionSampleConsumer creates a new adapter for Pion WebRTC objects that consume media samples.
//
// This constructor provides a clean way to wrap Pion WebRTC TrackLocalStaticSample objects or other
// Pion components that implement the CanConsumePionSamplePacket interface.
//
// Parameters:
//   - consumer: Any Pion WebRTC object that can consume media samples with automatic RTP handling
//
// Example:
//
//	trackLocal := // ... obtained from Pion WebRTC
//	adapter := NewPionSampleConsumer(trackLocal)
//	writer := NewIdentityAnyWriter(adapter)
func NewPionSampleConsumer(consumer CanConsumePionSamplePacket) *PionSampleConsumer {
	return &PionSampleConsumer{
		consumer: consumer,
	}
}

// Consume implements the CanConsume[media.Sample] interface by calling the underlying
// consumer's WriteSample() method.
//
// This method performs the adaptation from the standard CanConsume interface
// to Pion's WriteSample method signature, making Pion WebRTC TrackLocalStaticSample
// objects compatible with the universal writer system.
//
// The media.Sample is passed directly to the Pion object, which handles all the
// complex RTP processing internally including packetization, sequencing, timestamp
// conversion, and network transmission to connected WebRTC peers.
//
// Parameters:
//   - media.Sample: The media sample to be processed and transmitted
//
// Returns:
//   - error: Any error that occurred during sample processing or transmission
func (c *PionSampleConsumer) Consume(s media.Sample) error {
	return c.consumer.WriteSample(s)
}
