package generators

import (
	"context"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

type CanGeneratePionRTPPacket interface {
	ReadRTP(context.Context) (*rtp.Packet, interceptor.Attributes, error)
}

type PionRTPGenerator struct {
	generator CanGeneratePionRTPPacket // The Pion WebRTC object (e.g., RemoteTrack)
}

func NewPionRTPGenerator(generator CanGeneratePionRTPPacket) *PionRTPGenerator {
	return &PionRTPGenerator{
		generator: generator,
	}
}

func (g *PionRTPGenerator) Generate(ctx context.Context) (*rtp.Packet, error) {
	// for {
	// 	select {
	// 	case <-ctx.Done():
	// 		return nil, ctx.Err()
	// 	default:
	// 		p, _, err := g.generator.ReadRTP(ctx)
	// 		if err != nil {
	// 			return nil, err
	// 		}
	//
	// 		if p == nil || len(p.Payload) == 0 {
	// 			continue
	// 		}
	//
	// 		return p, nil
	// 	}
	// }

	p, _, err := g.generator.ReadRTP(ctx)
	return p, err
}
