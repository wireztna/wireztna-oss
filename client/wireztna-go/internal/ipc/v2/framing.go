package v2

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

var (
	// ErrEmptyFrame identifies a zero-length frame.
	ErrEmptyFrame = errors.New("IPC frame is empty")
	// ErrFrameTooLarge identifies a frame whose announced length exceeds its channel limit.
	ErrFrameTooLarge = errors.New("IPC frame exceeds its size limit")
)

// ReadFrame reads one uint32 big-endian length-prefixed frame. It checks the
// announced length before allocating the payload buffer.
func ReadFrame(reader io.Reader, limit uint32) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("frame reader is nil")
	}
	if limit == 0 {
		return nil, errors.New("frame limit must be positive")
	}

	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return nil, ErrEmptyFrame
	}
	if length > limit {
		return nil, fmt.Errorf("%w: announced=%d limit=%d", ErrFrameTooLarge, length, limit)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("read IPC frame payload: %w", err)
	}
	return payload, nil
}

// WriteFrame writes one uint32 big-endian length-prefixed frame.
func WriteFrame(writer io.Writer, payload []byte, limit uint32) error {
	if writer == nil {
		return errors.New("frame writer is nil")
	}
	if len(payload) == 0 {
		return ErrEmptyFrame
	}
	if uint64(len(payload)) > uint64(limit) {
		return fmt.Errorf("%w: actual=%d limit=%d", ErrFrameTooLarge, len(payload), limit)
	}
	if !utf8.Valid(payload) {
		return errors.New("IPC JSON frame is not valid UTF-8")
	}

	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(writer, header[:]); err != nil {
		return fmt.Errorf("write IPC frame header: %w", err)
	}
	if err := writeAll(writer, payload); err != nil {
		return fmt.Errorf("write IPC frame payload: %w", err)
	}
	return nil
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(payload) {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}

func readJSON(reader io.Reader, limit uint32, destination any) error {
	payload, err := ReadFrame(reader, limit)
	if err != nil {
		return err
	}
	return decodeStrict(payload, destination)
}

func writeJSON(writer io.Writer, limit uint32, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode IPC JSON frame: %w", err)
	}
	return WriteFrame(writer, payload, limit)
}
