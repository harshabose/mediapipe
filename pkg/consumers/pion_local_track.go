package consumers

import (
	"context"

	"github.com/pion/rtp"
)

type CanConsumePionRTPPackets interface {
	WriteRTP(*rtp.Packet) error
}

type PionRTPConsumer struct {
	consumer CanConsumePionRTPPackets
}

func NewPionRTPConsumer(consumer CanConsumePionRTPPackets) *PionRTPConsumer {
	return &PionRTPConsumer{consumer: consumer}
}

func (c *PionRTPConsumer) Consume(_ context.Context, p *rtp.Packet) error {
	return c.consumer.WriteRTP(p)
}
