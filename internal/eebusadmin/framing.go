package eebusadmin

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	adminFrameVersion    = 1
	adminFrameHeaderSize = 9
	maxAdminFrameBytes   = 64 << 10
)

var (
	errAdminFrameMalformed = errors.New("admin_frame_malformed")
	errAdminFrameOversized = errors.New("admin_frame_oversized")
)

func adminFrameHeader(length uint64) []byte {
	header := make([]byte, adminFrameHeaderSize)
	header[0] = adminFrameVersion
	binary.BigEndian.PutUint64(header[1:], length)
	return header
}

func encodeAdminFrame(payload []byte) ([]byte, error) {
	if len(payload) > maxAdminFrameBytes {
		return nil, errAdminFrameOversized
	}
	frame := make([]byte, adminFrameHeaderSize+len(payload))
	copy(frame, adminFrameHeader(uint64(len(payload))))
	copy(frame[adminFrameHeaderSize:], payload)
	return frame, nil
}

func readAdminFrame(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, errAdminFrameMalformed
	}
	header := make([]byte, adminFrameHeaderSize)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, errAdminFrameMalformed
	}
	if header[0] != adminFrameVersion {
		return nil, errAdminFrameMalformed
	}
	length := binary.BigEndian.Uint64(header[1:])
	if length > maxAdminFrameBytes {
		return nil, errAdminFrameOversized
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, errAdminFrameMalformed
	}
	return payload, nil
}
