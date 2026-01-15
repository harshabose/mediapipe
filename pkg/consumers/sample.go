package consumers

import (
	"context"

	"github.com/pion/webrtc/v4/pkg/media"
)

type CanConsumePionSamplePacket interface {
	WriteSample(media.Sample) error
}

type PionSampleConsumer struct {
	consumer CanConsumePionSamplePacket // The Pion WebRTC object (e.g., TrackLocalStaticSample)
}

func NewPionSampleConsumer(consumer CanConsumePionSamplePacket) *PionSampleConsumer {
	return &PionSampleConsumer{
		consumer: consumer,
	}
}

func (c *PionSampleConsumer) Consume(_ context.Context, s media.Sample) error {
	return c.consumer.WriteSample(s)
}
