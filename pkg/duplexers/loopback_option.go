package duplexers

type LoopBackOption = func(*LoopBack) error

func WithLoopBackPort(port uint16) LoopBackOption {
	return func(loopback *LoopBack) error {
		loopback.remote.port = int(port)
		return nil
	}
}

func WithBindPort(port uint16) LoopBackOption {
	return func(loopback *LoopBack) error {
		loopback.bind.port = int(port)
		return nil
	}
}
