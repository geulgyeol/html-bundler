package bundler

import (
	"bytes"
	"io"
	"testing"
)

type nopWriteCloser struct{ io.Writer }

func (nwc *nopWriteCloser) Close() error { return nil }

func TestReadBundleRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	writer, err := NewBundle(&nopWriteCloser{&buf}, map[string]any{"timestamp": int64(123)})
	if err != nil {
		t.Fatalf("NewBundle() error = %v", err)
	}

	if err := writer.WriteFrame(456, map[string]any{"url": "https://example.com", "id": "frame-1"}, []byte("<html>hello</html>")); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	bundle, err := ReadBundle(bytes.NewReader(buf.Bytes()), true)
	if err != nil {
		t.Fatalf("ReadBundle() error = %v", err)
	}

	if got, want := len(bundle.Frames), 1; got != want {
		t.Fatalf("frame count = %d; want %d", got, want)
	}

	if got, want := bundle.Footer.Payload.FrameCount, uint32(1); got != want {
		t.Fatalf("footer frame count = %d; want %d", got, want)
	}

	if got, want := bundle.Frames[0].Header.Timestamp, uint64(456); got != want {
		t.Fatalf("timestamp = %d; want %d", got, want)
	}

	if got, want := len(bundle.Frames[0].Payload.Content), 18; got != want {
		t.Fatalf("content length = %d; want %d", got, want)
	}

	if err := bundle.Validate(); err != nil {
		t.Fatalf("Bundle.Validate() error = %v", err)
	}
}
