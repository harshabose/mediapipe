package generators

import (
	"context"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

type CanGeneratePionRTPPacket interface {
	ReadRTP() (*rtp.Packet, interceptor.Attributes, error)
}

type PionRTPGenerator struct {
	generator CanGeneratePionRTPPacket // The Pion WebRTC object (e.g., RemoteTrack)
}

func NewPionRTPGenerator(generator CanGeneratePionRTPPacket) *PionRTPGenerator {
	return &PionRTPGenerator{
		generator: generator,
	}
}

func (g *PionRTPGenerator) Generate(_ context.Context) (*rtp.Packet, error) {
	p, _, err := g.generator.ReadRTP()
	return p, err
}
