package loopback

import "net"

type Option[T any] = func(*LoopBack[T]) error

func WithRandomBindPort[T any](loopback *LoopBack[T]) error {
	var err error
	if loopback.bindPort, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}); err != nil {
		return err
	}

	return nil
}

func WithBindPort[T any](port int) Option[T] {
	return func(loopback *LoopBack[T]) error {
		var err error
		if loopback.bindPort, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}); err != nil {
			return err
		}
		return nil
	}
}

func WithLoopBackPort[T any](port int) Option[T] {
	return func(loopback *LoopBack[T]) error {
		loopback.remote = &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
		return nil
	}
}
