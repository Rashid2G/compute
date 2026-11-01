package ws

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Channel IDs for binary multiplexed frames
const (
	ChannelStdin  uint8 = 0
	ChannelStdout uint8 = 1
	ChannelStderr uint8 = 2
	ChannelData   uint8 = 3
)

var (
	ErrBinaryFrameTooShort = errors.New("binary frame too short (min 5 bytes)")
	ErrInvalidChannel      = errors.New("invalid channel id")
)

// BinaryFrame represents a multiplexed byte stream chunk:
// | sid: uint32 BE (4 bytes) | channel: uint8 (1 byte) | payload ... |
type BinaryFrame struct {
	SID     uint32
	Channel uint8
	Payload []byte
}

// EncodeBinaryFrame encodes sid, channel, and payload into a binary frame slice.
func EncodeBinaryFrame(sid uint32, channel uint8, payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], sid)
	buf[4] = channel
	copy(buf[5:], payload)
	return buf
}

// DecodeBinaryFrame decodes a binary frame from wire bytes.
func DecodeBinaryFrame(data []byte) (*BinaryFrame, error) {
	if len(data) < 5 {
		return nil, ErrBinaryFrameTooShort
	}
	sid := binary.BigEndian.Uint32(data[0:4])
	channel := data[4]
	if channel > 3 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidChannel, channel)
	}
	return &BinaryFrame{
		SID:     sid,
		Channel: channel,
		Payload: data[5:],
	}, nil
}
