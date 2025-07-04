package rtpgenerator

import (
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// CanGeneratePionRTPPacket defines the interface for Pion WebRTC objects that produce RTP packets.
// This interface matches the signature used by Pion's RemoteTrack.ReadRTP() method,
// which returns additional interceptor attributes alongside the RTP packet.
//
// The interceptor.Attributes contain metadata about the packet processing pipeline,
// such as:
//   - Timestamp information for synchronization
//   - Quality metrics and statistics
//   - Security and authentication data
//   - Custom extension data from RTP extensions
//
// While this metadata can be valuable for advanced use cases, it's often unnecessary
// for core media streaming scenarios where only the RTP packet content matters.
//
// This interface is specifically designed for Pion WebRTC RemoteTrack objects and
// similar Pion components that follow this method signature pattern.
type CanGeneratePionRTPPacket interface {
	// ReadRTP returns the next RTP packet along with interceptor attributes.
	// This matches the exact signature of Pion WebRTC's RemoteTrack.ReadRTP() method.
	//
	// Returns:
	//   - *rtp.Packet: The RTP packet containing media data
	//   - interceptor.Attributes: Metadata from the interceptor processing pipeline
	//   - error: Any error that occurred during packet reading
	ReadRTP() (*rtp.Packet, interceptor.Attributes, error)
}

// PionRTPGenerator is an adapter that converts a CanGeneratePionRTPPacket into a CanGenerate[*rtp.Packet].
// This adapter allows Pion WebRTC objects (like RemoteTrack) to be used with the universal
// AnyReader system by discarding the interceptor attributes and exposing only the RTP packet.
//
// This is the most common adaptation needed when working with Pion WebRTC, as most streaming
// applications only need the RTP packet data and can ignore the interceptor metadata.
// The adapter provides a clean integration point between Pion's API and the universal
// media routing system.
//
// Example usage with Pion WebRTC:
//
//	remoteTrack := // ... obtained from Pion WebRTC peer connection
//	pionGenerator := &PionRTPGenerator{generator: remoteTrack}
//	reader := NewReader(pionGenerator, rtpTransformer)
//	buffered := NewBufferedReader(ctx, reader, 100)
type PionRTPGenerator struct {
	generator CanGeneratePionRTPPacket // The Pion WebRTC object (e.g., RemoteTrack)
}

// NewPionRTPGenerator creates a new adapter for Pion WebRTC objects that produce RTP packets.
//
// This constructor provides a clean way to wrap Pion WebRTC RemoteTrack objects or other
// Pion components that implement the CanGeneratePionRTPPacket interface.
//
// Parameters:
//   - generator: Any Pion WebRTC object that can produce RTP packets with interceptor attributes
//
// Example:
//
//	remoteTrack := // ... obtained from Pion WebRTC
//	adapter := NewPionRTPGenerator(remoteTrack)
//	reader := NewReader(adapter, transformer)
func NewPionRTPGenerator(generator CanGeneratePionRTPPacket) *PionRTPGenerator {
	return &PionRTPGenerator{
		generator: generator,
	}
}

// Generate implements the CanGenerate[*rtp.Packet] interface by calling the underlying
// generator's ReadRTP() method and discarding the interceptor attributes.
//
// This method performs the adaptation from Pion's three-return-value signature
// to the standard two-return-value CanGenerate interface, making Pion WebRTC
// objects compatible with the universal reader system.
//
// The interceptor.Attributes are intentionally discarded as they're typically
// not needed for core streaming use cases. If you need access to interceptor
// metadata, you should work directly with the Pion API instead of using this adapter.
//
// Returns:
//   - *rtp.Packet: The RTP packet from the Pion object
//   - error: Any error that occurred during reading
func (g *PionRTPGenerator) Generate() (*rtp.Packet, error) {
	p, _, err := g.generator.ReadRTP()
	return p, err
}
