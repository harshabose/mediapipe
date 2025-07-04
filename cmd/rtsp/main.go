package main

import (
	"context"

	"github.com/harshabose/mediasink/pkg/rtsp"
)

/* EXAMPLE INPUT
ffmpeg -f avfoundation -framerate 30 -i "0" \
  -c:v libx264 \
  -tune zerolatency \
  -preset ultrafast \
  -g 30 \
  -b:v 1000k \
  -f rtsp rtsp://localhost:8554/webcam
*/

/* EXAMPLE OUTPUT
ffplay -fflags nobuffer -flags low_delay \
	-framedrop -avioflags direct
	rtsp://localhost:8554/webcam
*/

func main() {
	<-rtsp.NewServer(context.Background(), nil).ServeAndWait()
}
