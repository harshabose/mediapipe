package duplexers

import (
	"context"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

// SampleBuilder is H264 specific builder which builds media.Sample using samplebuilder.SampleBuilder
type SampleBuilder struct {
	s   *samplebuilder.SampleBuilder
	mux sync.Mutex
}

func NewSampleBuilder(jitter uint16, samplerate uint32) *SampleBuilder {
	return &SampleBuilder{
		s: samplebuilder.New(jitter, &codecs.H264Packet{}, samplerate),
	}
}

func (s *SampleBuilder) Consume(_ context.Context, data *rtp.Packet) error {
	s.mux.Lock()
	defer s.mux.Unlock()

	s.s.Push(data)
	return nil
}

func (s *SampleBuilder) Generate(ctx context.Context) (*media.Sample, error) {
	s.mux.Lock()
	defer s.mux.Unlock()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			sample := s.s.Pop()
			if sample == nil {
				continue
			}

			return sample, nil
		}
	}
}

func (s *SampleBuilder) Close() {
	s.s.Flush()
}
