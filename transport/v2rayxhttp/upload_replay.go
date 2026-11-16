package v2rayxhttp

import (
	"io"
	"sync"
)

// uploadRecorder is the stable write side of a stream-one conn. It forwards
// every Write to the current upload pipe while recording a copy, so a dial
// that times out on a dead pool member can be replayed on a fresh one.
//
// Why this exists: stream-one carries its upload body on the same HTTP/2
// stream as the handshake, so unlike the download GET it could never be
// replayed — a warm-member timeout failed the whole dial and the app waited
// out the full budget per new connection (a Shorts swipe). The bytes at stake
// are only the pre-headers writes (the VLESS/encryption handshake), so the
// recording is bounded by construction: rotation freezes it, success discards
// it.
//
// Concurrency contract: the mutex is never held across a blocking pipe Write.
// Rotation and a blocked writer therefore cannot deadlock; a writer released
// by a rotation sees the generation flip and reports success (its bytes are
// in the snapshot and will be replayed), while a writer released by a real
// break (deadline, dial cancel, Close) reports the break error.
type uploadRecorder struct {
	mu          sync.Mutex
	buf         []byte
	rotated     bool
	passThrough bool
	closed      bool
	breakErr    error
	dst         *io.PipeWriter
	live        *io.PipeReader
}

func newUploadRecorder(dst *io.PipeWriter, live *io.PipeReader) *uploadRecorder {
	return &uploadRecorder{dst: dst, live: live}
}

func (u *uploadRecorder) Write(b []byte) (int, error) {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if u.rotated || u.passThrough {
		w := u.dst
		u.mu.Unlock()
		return w.Write(b)
	}
	u.buf = append(u.buf, b...)
	w := u.dst
	u.mu.Unlock()
	if _, err := w.Write(b); err == nil {
		return len(b), nil
	} else {
		u.mu.Lock()
		rotated, passThrough, broken, breakErr := u.rotated, u.passThrough, u.breakErr != nil, u.breakErr
		u.mu.Unlock()
		// A real break (deadline, dial cancel) always wins: the stream is
		// being torn down, replay or not.
		if broken {
			if breakErr != nil {
				return 0, breakErr
			}
			return 0, err
		}
		// Released by the rotation: the bytes are already in the snapshot
		// and ride the replay, so the stream stays alive.
		if rotated || passThrough {
			return len(b), nil
		}
		return 0, err
	}
}

// rotate freezes the recording and swaps the destination to a fresh pipe.
// The returned reader serves the snapshot first, then the live tail of the
// new pipe; the caller replays it as the body of the retry request. The old
// pipe is closed to release writers blocked on the dead member — their Write
// maps to success via the generation check above.
func (u *uploadRecorder) rotate() (snapshot []byte, reader *io.PipeReader, writer *io.PipeWriter) {
	u.mu.Lock()
	snapshot = u.buf
	u.buf = nil
	u.rotated = true
	old := u.live
	pr, pw := io.Pipe()
	u.dst, u.live = pw, pr
	u.mu.Unlock()
	old.CloseWithError(io.ErrClosedPipe)
	return snapshot, pr, pw
}

// breakWrites releases writers blocked in the current pipe with err (write
// deadline, dial cancel). Unlike rotate it marks the stream broken, so the
// released Writes fail instead of being absorbed.
func (u *uploadRecorder) breakWrites(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.breakErr == nil {
		u.breakErr = err
	}
	if u.live != nil {
		u.live.CloseWithError(err)
	}
}

// liveReader returns the pipe whose close releases the currently blocked
// writers, for the write-deadline arm.
func (u *uploadRecorder) liveReader() *io.PipeReader {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.live
}

// discard drops the recording once the stream is up: no retry can happen
// past the first response headers, so keeping the prefix would only leak.
func (u *uploadRecorder) discard() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.buf = nil
	// The stream is live, so no retry can ever need the prefix again. Without this the
	// recorder keeps appending every later upload byte for the life of the connection.
	u.passThrough = true
}

func (u *uploadRecorder) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	u.closed = true
	return u.dst.Close()
}
