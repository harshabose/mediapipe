package consumers

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

type RTSPClientOption func(*RTSPClient) error

type PacketisationMode uint8

const (
	// PacketisationMode0 represents Single NAL Unit Mode.
	// Each RTP packet contains exactly one NAL unit. This is the simplest mode
	// but may not be optimal for larger NAL units that exceed MTU size.
	// Used for basic H.264 streaming
	PacketisationMode0 PacketisationMode = 0

	// PacketisationMode1 represents Non-Interleaved Mode.
	// Allows fragmentation of large NAL units across multiple RTP packets
	// and aggregation of small NAL units into single RTP packets.
	// This is the most commonly used
	PacketisationMode1 PacketisationMode = 1

	// PacketisationMode2 represents Interleaved Mode.
	// Similar to Mode 1 but allows interleaving of packets from different
	// NAL units. Rarely used due to implementation complexity.
	PacketisationMode2 PacketisationMode = 2
)

func WithH264Options(packetisationMode PacketisationMode, spsBase64, ppsBase64 string) RTSPClientOption {
	return func(client *RTSPClient) error {
		sps, err := base64.StdEncoding.DecodeString(spsBase64)
		if err != nil {
			return fmt.Errorf("failed to decode sps (err: %w)", err)
		}
		pps, err := base64.StdEncoding.DecodeString(ppsBase64)
		if err != nil {
			return fmt.Errorf("failed to decode pps (err: %w)", err)
		}

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

func WithVP8Option() RTSPClientOption {
	return func(client *RTSPClient) error {
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

func WithOpusOptions(channelCount int) RTSPClientOption {
	return func(client *RTSPClient) error {
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

func WithOptionsFromRemote(remote *webrtc.TrackRemote) RTSPClientOption {
	switch remote.Codec().MimeType {
	case webrtc.MimeTypeH264:
		return withH264OptionsFromRemote(remote)
	case webrtc.MimeTypeVP8:
		return withVP8OptionsFromRemote(remote)
	case webrtc.MimeTypeOpus:
		return withOpusOptionsFromRemote(remote)
	default:
		return func(_ *RTSPClient) error {
			return fmt.Errorf("unsupported codec type: %s", remote.Codec().MimeType)
		}
	}
}

func withH264OptionsFromRemote(remote *webrtc.TrackRemote) RTSPClientOption {
	return func(client *RTSPClient) error {
		sps, pps, err := parseSPSPPS(remote.Codec().SDPFmtpLine)
		if err != nil {
			return fmt.Errorf("failed to parse H.264 SPS/PPS: %w", err)
		}

		packetisationMode, err := parsePacketisationMode(remote.Codec().SDPFmtpLine)
		if err != nil {
			return fmt.Errorf("failed to parse H.264 packetization mode: %w", err)
		}

		if remote.Codec().PayloadType != 102 {
			return fmt.Errorf("h.264 payload type mismatch: expected 102, got %d",
				remote.Codec().PayloadType)
		}

		return WithH264Options(PacketisationMode(packetisationMode), sps, pps)(client)
	}
}

func withVP8OptionsFromRemote(remote *webrtc.TrackRemote) RTSPClientOption {
	return func(client *RTSPClient) error {
		if remote.Codec().PayloadType != 96 {
			return fmt.Errorf("VP8 payload type mismatch: expected 96, got %d",
				remote.Codec().PayloadType)
		}

		return WithVP8Option()(client)
	}
}

func withOpusOptionsFromRemote(remote *webrtc.TrackRemote) RTSPClientOption {
	return func(client *RTSPClient) error {
		if remote.Codec().PayloadType != 111 {
			return fmt.Errorf("opus payload type mismatch: expected 111, got %d",
				remote.Codec().PayloadType)
		}

		return WithOpusOptions(int(remote.Codec().Channels))(client)
	}
}

func parseSPSPPS(sdpFmtpLine string) (sps, pps string, err error) {
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
		return "", "", errors.New("sprop-parameter-sets not found in SDP fmtp line")
	}

	// Split into individual parameter sets (typically SPS and PPS)
	parameterSets := strings.Split(spropParameterSets, ",")
	if len(parameterSets) != 2 {
		return "", "", fmt.Errorf("expected 2 parameter sets (SPS, PPS), got %d",
			len(parameterSets))
	}

	return strings.TrimSpace(parameterSets[0]), strings.TrimSpace(parameterSets[1]), nil
}

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
