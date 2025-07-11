package duplexers

import (
	"net"
)

type LoopBackOption = func(*LoopBack) error

func WithRandomBindPort(loopback *LoopBack) error {
	var err error
	if loopback.bindPort, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}); err != nil {
		return err
	}

	return nil
}

func WithBindPort(port int) LoopBackOption {
	return func(loopback *LoopBack) error {
		var err error
		if loopback.bindPort, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}); err != nil {
			return err
		}
		return nil
	}
}

func WithLoopBackPort(port int) LoopBackOption {
	return func(loopback *LoopBack) error {
		loopback.remote = &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
		return nil
	}
}
