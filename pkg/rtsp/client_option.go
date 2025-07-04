package rtsp

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/webrtc/v4"
)

// ClientOption represents a configuration function that can customise the client
// during initialisation. Options are applied after basic client setup but before
// validation and connection attempts.
type ClientOption func(*Client) error

// PacketisationMode represents H.264 packetization modes as defined in RFC 6184.
// These modes determine how H.264 NAL units are packaged into RTP packets,
// affecting compatibility and performance characteristics.
type PacketisationMode uint8

const (
	// PacketisationMode0 represents Single NAL Unit Mode.
	// Each RTP packet contains exactly one NAL unit. This is the simplest mode
	// but may not be optimal for larger NAL units that exceed MTU size.
	// Used for basic H.264 streaming with minimal complexity.
	PacketisationMode0 PacketisationMode = 0

	// PacketisationMode1 represents Non-Interleaved Mode.
	// Allows fragmentation of large NAL units across multiple RTP packets
	// and aggregation of small NAL units into single RTP packets.
	// This is the most commonly used mode for efficient H.264 streaming.
	PacketisationMode1 PacketisationMode = 1

	// PacketisationMode2 represents Interleaved Mode.
	// Similar to Mode 1 but allows interleaving of packets from different
	// NAL units. Rarely used due to implementation complexity.
	PacketisationMode2 PacketisationMode = 2
)

// WithH264Options creates a ClientOption that configures H.264 video streaming
// with the specified packetization mode and codec parameters.
//
// This option adds H.264 video media description to the RTSP session with
// the provided SPS (Sequence Parameter Set) and PPS (Picture Parameter Set).
// These parameters are essential for H.264 decoding and must match the
// encoder configuration.
//
// Parameters:
//   - packetisationMode: H.264 RTP packetisation mode (typically Mode 1)
//   - sps: Sequence Parameter Set (contains video dimensions, profile, level)
//   - pps: Picture Parameter Set (contains entropy coding parameters)
//
// The option uses payload type 102 following Pion WebRTC conventions for
// consistency across the media pipeline.
//
// Example:
//
//	sps := []byte{0x67, 0x42, 0x80, 0x1e, ...} // From encoder
//	pps := []byte{0x68, 0xce, 0x3c, 0x80}      // From encoder
//
//	client, err := rtsp.NewClient(ctx, config, nil,
//	    rtsp.WithH264Options(rtsp.PacketisationMode1, sps, pps))
func WithH264Options(packetisationMode PacketisationMode, sps, pps []byte) ClientOption {
	return func(client *Client) error {
		media := &description.Media{
			Type: description.MediaTypeVideo,
			Formats: []format.Format{&format.H264{
				PayloadTyp:        102, // Following Pion's convention for consistency
				PacketizationMode: int(packetisationMode),
				SPS:               sps,
				PPS:               pps,
			}},
		}

		client.AppendRTSPMediaDescription(media)
		return nil
	}
}

// WithVP8Option creates a ClientOption that configures VP8 video streaming.
//
// This option adds VP8 video media description to the RTSP session with
// standard configuration. VP8 is a royalty-free video codec that provides
// good compression efficiency and is widely supported.
//
// The option uses payload type 96 following Pion WebRTC conventions.
// MaxFR (maximum frame rate) and MaxFS (maximum frame size) are set to nil,
// allowing the decoder to handle any frame rate and size within reasonable limits.
//
// Example:
//
//	client, err := rtsp.NewClient(ctx, config, nil,
//	    rtsp.WithVP8Option())
func WithVP8Option() ClientOption {
	return func(client *Client) error {
		media := &description.Media{
			Type: description.MediaTypeVideo,
			Formats: []format.Format{&format.VP8{
				PayloadTyp: 96,  // Following Pion's convention
				MaxFR:      nil, // No frame rate limit
				MaxFS:      nil, // No frame size limit
			}},
		}

		client.AppendRTSPMediaDescription(media)
		return nil
	}
}

// WithOpusOptions creates a ClientOption that configures Opus audio streaming
// with the specified channel configuration.
//
// Opus is a high-quality, low-latency audio codec that supports both speech
// and music with excellent compression. It's the preferred audio codec for
// real-time communication applications.
//
// Parameters:
//   - channelCount: Number of audio channels (1 for mono, 2 for stereo)
//
// The option uses payload type 111 following Pion WebRTC conventions for
// consistency across the media pipeline.
//
// Example:
//
//	// Stereo audio configuration
//	client, err := rtsp.NewClient(ctx, config, nil,
//	    rtsp.WithOpusOptions(2))
//
//	// Mono audio configuration
//	client, err := rtsp.NewClient(ctx, config, nil,
//	    rtsp.WithOpusOptions(1))
func WithOpusOptions(channelCount int) ClientOption {
	return func(client *Client) error {
		media := &description.Media{
			Type: description.MediaTypeAudio,
			Formats: []format.Format{&format.Opus{
				PayloadTyp:   111, // Following Pion's convention
				ChannelCount: channelCount,
			}},
		}

		client.AppendRTSPMediaDescription(media)
		return nil
	}
}

// WithOptionsFromRemote creates a ClientOption that automatically configures
// the RTSP client based on a WebRTC RemoteTrack's codec parameters.
//
// This is a convenience function that simplifies the integration between
// WebRTC and RTSP by automatically detecting the codec type and extracting
// the necessary parameters from the remote track's SDP information.
//
// Supported codecs:
//   - H.264: Extracts SPS, PPS, and packetization mode from SDP
//   - VP8: Uses standard VP8 configuration
//   - Opus: Extracts channel count from codec parameters
//
// The function validates that payload types match Pion conventions to ensure
// compatibility across the media pipeline.
//
// Parameters:
//   - remote: WebRTC RemoteTrack containing codec information
//
// Returns an error if:
//   - The codec type is not supported
//   - Payload types don't match expected conventions
//   - SDP parameter parsing fails (for H.264)
//
// Example usage in WebRTC-to-RTSP pipeline:
//
//	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
//	    client, err := rtsp.NewClient(ctx, config, nil,
//	        rtsp.WithOptionsFromRemote(track))
//	    if err != nil {
//	        log.Printf("Failed to create RTSP client: %v", err)
//	        return
//	    }
//	    defer client.Close()
//
//	    // Set up pipeline: WebRTC → Reader → BufferedWriter → RTSP
//	    pionGenerator := mediasink.NewPionRTPGenerator(track)
//	    reader := mediasink.NewReader(pionGenerator, transformer)
//	    writer := mediasink.NewAnyWriter(client, transformer)
//	    buffered := mediasink.NewBufferedWriter(ctx, writer, 100)
//	})
func WithOptionsFromRemote(remote *webrtc.TrackRemote) ClientOption {
	switch remote.Codec().MimeType {
	case webrtc.MimeTypeH264:
		return withH264OptionsFromRemote(remote)
	case webrtc.MimeTypeVP8:
		return withVP8OptionsFromRemote(remote)
	case webrtc.MimeTypeOpus:
		return withOpusOptionsFromRemote(remote)
	default:
		return func(client *Client) error {
			return fmt.Errorf("unsupported codec type: %s", remote.Codec().MimeType)
		}
	}
}

// withH264OptionsFromRemote extracts H.264 parameters from a WebRTC RemoteTrack
// and creates the appropriate H.264 configuration.
//
// This function parses the SDP fmtp line to extract:
//   - SPS and PPS from sprop-parameter-sets
//   - Packetization mode from packetization-mode parameter
//
// It validates that the payload type matches the expected convention (102)
// to ensure compatibility with the rest of the media pipeline.
func withH264OptionsFromRemote(remote *webrtc.TrackRemote) ClientOption {
	return func(client *Client) error {
		// Extract SPS and PPS from SDP parameters
		sps, pps, err := parseSPSPPS(remote.Codec().SDPFmtpLine)
		if err != nil {
			return fmt.Errorf("failed to parse H.264 SPS/PPS: %w", err)
		}

		// Extract packetization mode
		packetisationMode, err := parsePacketisationMode(remote.Codec().SDPFmtpLine)
		if err != nil {
			return fmt.Errorf("failed to parse H.264 packetization mode: %w", err)
		}

		// Validate payload type convention
		if remote.Codec().PayloadType != 102 {
			return fmt.Errorf("H.264 payload type mismatch: expected 102, got %d",
				remote.Codec().PayloadType)
		}

		// Apply H.264 configuration
		return WithH264Options(PacketisationMode(packetisationMode), sps, pps)(client)
	}
}

// withVP8OptionsFromRemote configures VP8 streaming based on a WebRTC RemoteTrack.
//
// This function validates the payload type and applies standard VP8 configuration.
// VP8 has fewer parameters than H.264, so the configuration is straightforward.
func withVP8OptionsFromRemote(remote *webrtc.TrackRemote) ClientOption {
	return func(client *Client) error {
		// Validate payload type convention
		if remote.Codec().PayloadType != 96 {
			return fmt.Errorf("VP8 payload type mismatch: expected 96, got %d",
				remote.Codec().PayloadType)
		}

		// Apply VP8 configuration
		return WithVP8Option()(client)
	}
}

// withOpusOptionsFromRemote configures Opus audio streaming based on a WebRTC RemoteTrack.
//
// This function extracts the channel count from the codec parameters and
// validates the payload type convention.
func withOpusOptionsFromRemote(remote *webrtc.TrackRemote) ClientOption {
	return func(client *Client) error {
		// Validate payload type convention
		if remote.Codec().PayloadType != 111 {
			return fmt.Errorf("Opus payload type mismatch: expected 111, got %d",
				remote.Codec().PayloadType)
		}

		// Apply Opus configuration with channel count from remote track
		return WithOpusOptions(int(remote.Codec().Channels))(client)
	}
}

// parseSPSPPS extracts H.264 Sequence Parameter Set (SPS) and Picture Parameter Set (PPS)
// from the SDP fmtp line's sprop-parameter-sets attribute.
//
// The sprop-parameter-sets parameter contains base64-encoded SPS and PPS data
// separated by commas. These parameters are essential for H.264 decoding as they
// contain crucial information about video dimensions, encoding profile, and
// entropy coding parameters.
//
// Parameters:
//   - sdpFmtpLine: Complete SDP fmtp line containing H.264 parameters
//
// Returns:
//   - sps: Decoded Sequence Parameter Set bytes
//   - pps: Decoded Picture Parameter Set bytes
//   - err: Error if parsing or decoding fails
//
// The function expects exactly two-parameter sets in the standard H.264 configuration.
func parseSPSPPS(sdpFmtpLine string) (sps, pps []byte, err error) {
	params := strings.Split(sdpFmtpLine, ";")
	var spropParameterSets string

	// Find sprop-parameter-sets parameter
	for _, param := range params {
		param = strings.TrimSpace(param)
		if strings.HasPrefix(param, "sprop-parameter-sets=") {
			spropParameterSets = strings.TrimPrefix(param, "sprop-parameter-sets=")
			break
		}
	}

	if spropParameterSets == "" {
		return nil, nil, errors.New("sprop-parameter-sets not found in SDP fmtp line")
	}

	// Split into individual parameter sets (typically SPS and PPS)
	parameterSets := strings.Split(spropParameterSets, ",")
	if len(parameterSets) != 2 {
		return nil, nil, fmt.Errorf("expected 2 parameter sets (SPS, PPS), got %d",
			len(parameterSets))
	}

	// Decode base64-encoded SPS
	sps, err = base64.StdEncoding.DecodeString(strings.TrimSpace(parameterSets[0]))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode SPS from base64: %w", err)
	}

	// Decode base64-encoded PPS
	pps, err = base64.StdEncoding.DecodeString(strings.TrimSpace(parameterSets[1]))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode PPS from base64: %w", err)
	}

	return sps, pps, nil
}

// parsePacketisationMode extracts the H.264 packetization mode from the SDP fmtp line.
//
// The packetization-mode parameter specifies how H.264 NAL units should be
// packaged into RTP packets. This affects compatibility and performance
// characteristics of the stream.
//
// Parameters:
//   - sdpFmtpLine: Complete SDP fmtp line containing H.264 parameters
//
// Returns:
//   - int: Packetization mode value (0, 1, or 2)
//   - error: Error if parameter is not found or invalid
//
// If packetization-mode is not explicitly specified, H.264 defaults to mode 0.
func parsePacketisationMode(sdpFmtpLine string) (int, error) {
	params := strings.Split(sdpFmtpLine, ";")

	// Find packetization-mode parameter
	for _, param := range params {
		param = strings.TrimSpace(param)
		if strings.HasPrefix(param, "packetization-mode=") {
			modeStr := strings.TrimPrefix(param, "packetization-mode=")
			mode, err := strconv.Atoi(strings.TrimSpace(modeStr))
			if err != nil {
				return 0, fmt.Errorf("invalid packetization-mode value '%s': %w", modeStr, err)
			}

			if mode == 0 || mode == 1 || mode == 2 {
				return mode, nil
			}

			fmt.Printf("invalid packetisation-mode value: should be either 0, 1 or 2 but has %d", mode)
		}
	}

	// Default to mode 0 if not specified (per RFC 6184)
	fmt.Printf("defaulting to packetisation-mode 0")
	return 0, nil
}
