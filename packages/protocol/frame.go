package protocol

import (
	"encoding/binary"
	"errors"
	"io"
)

// PTY streams are framed so that terminal data and resize requests can share
// one yamux stream without escaping: each record is
//
//	1 byte  kind   (FrameData | FrameResize)
//	4 bytes length (big endian)
//	length bytes payload
//
// Data payload is raw bytes; Resize payload is 2x uint16 (cols, rows).
const (
	FrameData   byte = 0
	FrameResize byte = 1
)

const maxFrame = 1 << 20

// WriteFrame writes one record.
func WriteFrame(w io.Writer, kind byte, payload []byte) error {
	if len(payload) > maxFrame {
		return errors.New("frame too large")
	}
	hdr := make([]byte, 5)
	hdr[0] = kind
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads one record.
func ReadFrame(r io.Reader) (kind byte, payload []byte, err error) {
	hdr := make([]byte, 5)
	if _, err = io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, errors.New("frame too large")
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

// EncodeResize / DecodeResize pack a PTY size into a Resize frame payload.
func EncodeResize(cols, rows int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:], uint16(cols))
	binary.BigEndian.PutUint16(b[2:], uint16(rows))
	return b
}

func DecodeResize(b []byte) (cols, rows int, ok bool) {
	if len(b) != 4 {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint16(b[0:])), int(binary.BigEndian.Uint16(b[2:])), true
}
