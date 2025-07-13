module github.com/harshabose/mediapipe

go 1.24.1

require (
	github.com/bluenviron/gortsplib/v4 v4.14.1
	github.com/coder/websocket v1.8.13
	github.com/emirpasic/gods/v2 v2.0.0-alpha
	github.com/harshabose/tools v0.0.0
	github.com/hashicorp/golang-lru/v2 v2.0.7
	github.com/pion/interceptor v0.1.40
	github.com/pion/rtp v1.8.19
	github.com/pion/webrtc/v4 v4.1.2
	golang.org/x/time v0.12.0
)

require (
	github.com/bluenviron/mediacommon/v2 v2.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pion/datachannel v1.5.10 // indirect
	github.com/pion/dtls/v3 v3.0.6 // indirect
	github.com/pion/ice/v4 v4.0.10 // indirect
	github.com/pion/logging v0.2.3 // indirect
	github.com/pion/mdns/v2 v2.0.7 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.15 // indirect
	github.com/pion/sctp v1.8.39 // indirect
	github.com/pion/sdp/v3 v3.0.13 // indirect
	github.com/pion/srtp/v3 v3.0.5 // indirect
	github.com/pion/stun/v3 v3.0.0 // indirect
	github.com/pion/transport/v3 v3.0.7 // indirect
	github.com/pion/turn/v4 v4.0.0 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/crypto v0.38.0 // indirect
	golang.org/x/net v0.40.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
)

replace (
	github.com/harshabose/jwtAuth => ../jwtAuth
	github.com/harshabose/tools => ../tools
)
