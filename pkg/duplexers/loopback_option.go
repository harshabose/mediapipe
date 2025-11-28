package duplexers

type LoopBackOption = func(*LocalUDP) error

func WithLoopBackPort(port uint16) LoopBackOption {
	return func(loopback *LocalUDP) error {
		loopback.remote.port = int(port)
		return nil
	}
}

func WithBindPort(port uint16) LoopBackOption {
	return func(loopback *LocalUDP) error {
		loopback.bind.port = int(port)
		return nil
	}
}
