package encoder

import (
	"av1go/obu"
	"errors"
	"fmt"
)

// State tracks the current state of the encoder.
type State int

const (
	// StateNew is the initial state before encoding begins.
	StateNew State = iota
	// StateReady means the encoder is configured and ready to accept frames.
	StateReady
	// StateEncoding means the encoder is actively processing frames.
	StateEncoding
	// StateFlushing means the encoder is draining buffered frames.
	StateFlushing
	// StateDone means the encoder has finished and been flushed.
	StateDone
)

// Packet represents an encoded output packet (one or more OBUs forming
// a temporal unit).
type Packet struct {
	Data      []byte    // Encoded OBU data.
	PTS       int64     // Presentation timestamp.
	DTS       int64     // Decode timestamp.
	Duration  int64     // Frame duration in timebase units.
	FrameType FrameType // Type of the encoded frame.
	IsKeyFrame bool    // True if this packet contains a keyframe.
	Size      int       // Size of encoded data in bytes.
}

// Frame represents a raw input video frame to be encoded.
type Frame struct {
	// Y, U, V contain the planar pixel data.
	// For monochrome, U and V are nil.
	Y []byte
	U []byte
	V []byte

	// Strides for each plane.
	StrideY int
	StrideU int
	StrideV int

	// Width and Height of the frame.
	Width  uint32
	Height uint32

	// PTS is the presentation timestamp for this frame.
	PTS int64

	// Duration is the frame duration in timebase units.
	Duration int64

	// ForceKeyFrame forces this frame to be encoded as a keyframe.
	ForceKeyFrame bool
}

// Encoder is an AV1 video encoder.
//
// Usage:
//
//	cfg := encoder.DefaultConfig(1920, 1080)
//	enc, err := encoder.New(cfg)
//	// Send frames:
//	err = enc.SendFrame(frame)
//	// Receive packets:
//	pkt, err := enc.ReceivePacket()
//	// Flush at end of stream:
//	err = enc.Flush()
type Encoder struct {
	config         Config
	state          State
	sequenceHeader *obu.SequenceHeader
	frameCount     int64
	obuWriter      *obu.Writer

	// packetQueue holds encoded packets ready for output.
	packetQueue []*Packet
}

// New creates a new AV1 encoder with the given configuration.
// Returns an error if the configuration is invalid.
func New(cfg Config) (*Encoder, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("encoder: invalid config: %w", err)
	}

	enc := &Encoder{
		config:    cfg,
		state:     StateReady,
		obuWriter: obu.NewWriter(),
	}

	// Generate sequence header from config.
	enc.sequenceHeader = cfg.SequenceHeader()

	return enc, nil
}

// Config returns the encoder's current configuration.
func (e *Encoder) Config() Config {
	return e.config
}

// State returns the current encoder state.
func (e *Encoder) State() State {
	return e.state
}

// SequenceHeader returns the encoder's sequence header.
func (e *Encoder) SequenceHeader() *obu.SequenceHeader {
	return e.sequenceHeader
}

// WriteSequenceHeaderOBU generates the complete Sequence Header OBU bytes.
// This is useful for writing the sequence header to a container or
// for out-of-band signaling.
func (e *Encoder) WriteSequenceHeaderOBU() ([]byte, error) {
	payload, err := obu.WriteSequenceHeader(e.sequenceHeader)
	if err != nil {
		return nil, fmt.Errorf("encoder: failed to write sequence header: %w", err)
	}
	return obu.MakeOBU(obu.Header{
		Type:    obu.TypeSequenceHeader,
		HasSize: true,
	}, payload)
}

// SendFrame submits a raw frame for encoding. The encoder may buffer
// frames internally (controlled by LagInFrames). Use ReceivePacket to
// retrieve encoded output.
//
// Returns ErrEncoderFlushed if the encoder has already been flushed.
func (e *Encoder) SendFrame(frame *Frame) error {
	switch e.state {
	case StateNew:
		return errors.New("encoder: encoder not initialized")
	case StateFlushing, StateDone:
		return ErrEncoderFlushed
	case StateReady:
		e.state = StateEncoding
	case StateEncoding:
		// Continue.
	}

	if frame == nil {
		return errors.New("encoder: frame is nil")
	}

	if frame.Width != e.config.Width || frame.Height != e.config.Height {
		return fmt.Errorf("encoder: frame dimensions %dx%d do not match config %dx%d",
			frame.Width, frame.Height, e.config.Width, e.config.Height)
	}

	// Determine frame type.
	isKey := frame.ForceKeyFrame || e.frameCount == 0
	if e.config.KeyFrameInterval > 0 && e.frameCount > 0 {
		if e.frameCount%int64(e.config.KeyFrameInterval) == 0 {
			isKey = true
		}
	}

	// Build the temporal unit for this frame.
	pkt, err := e.encodeFrame(frame, isKey)
	if err != nil {
		return err
	}

	e.packetQueue = append(e.packetQueue, pkt)
	e.frameCount++

	return nil
}

// ReceivePacket retrieves the next encoded packet. Returns nil if no
// packets are available. The caller should call ReceivePacket in a loop
// after each SendFrame or Flush call.
func (e *Encoder) ReceivePacket() *Packet {
	if len(e.packetQueue) == 0 {
		return nil
	}
	pkt := e.packetQueue[0]
	e.packetQueue = e.packetQueue[1:]
	return pkt
}

// Flush signals end of input. The encoder will output any buffered frames.
// After flushing, no more frames can be sent.
func (e *Encoder) Flush() error {
	switch e.state {
	case StateDone:
		return nil
	case StateNew:
		return errors.New("encoder: encoder not initialized")
	case StateFlushing:
		// Already flushing.
	default:
		e.state = StateFlushing
	}

	// In the current implementation frames are output immediately,
	// so flush simply transitions to Done.
	e.state = StateDone
	return nil
}

// Close releases encoder resources.
func (e *Encoder) Close() {
	e.state = StateDone
	e.packetQueue = nil
	e.obuWriter.Reset()
}

// encodeFrame produces a temporal unit for a single frame.
// Currently produces a skeleton temporal unit with TD + sequence header
// (for keyframes) + a minimal frame OBU.
func (e *Encoder) encodeFrame(frame *Frame, isKey bool) (*Packet, error) {
	e.obuWriter.Reset()

	// Every temporal unit starts with a Temporal Delimiter OBU.
	if err := e.obuWriter.WriteTemporalDelimiter(); err != nil {
		return nil, fmt.Errorf("encoder: failed to write TD: %w", err)
	}

	// Keyframes include the sequence header.
	if isKey {
		payload, err := obu.WriteSequenceHeader(e.sequenceHeader)
		if err != nil {
			return nil, fmt.Errorf("encoder: failed to write sequence header: %w", err)
		}
		if err := e.obuWriter.WriteOBU(obu.Header{
			Type:    obu.TypeSequenceHeader,
			HasSize: true,
		}, payload); err != nil {
			return nil, fmt.Errorf("encoder: failed to write SH OBU: %w", err)
		}
	}

	// Determine frame type and build frame header params.
	ft := FrameTypeInter
	if isKey {
		ft = FrameTypeKey
	}

	fhp := obu.DefaultKeyFrameParams(uint8(e.config.QP))
	fhp.FrameType = ft
	if !isKey {
		fhp = obu.DefaultInterFrameParams(uint8(e.config.QP), uint32(e.frameCount))
	}

	// Encode Frame OBU (frame header + tile group combined).
	te := NewTileEncoder(e.sequenceHeader, fhp, &e.config)
	framePayload, err := te.EncodeFrame(frame, isKey)
	if err != nil {
		return nil, fmt.Errorf("encoder: failed to encode frame: %w", err)
	}

	if err := e.obuWriter.WriteOBU(obu.Header{
		Type:    obu.TypeFrame,
		HasSize: true,
	}, framePayload); err != nil {
		return nil, fmt.Errorf("encoder: failed to write frame OBU: %w", err)
	}

	data := e.obuWriter.Bytes()

	return &Packet{
		Data:       data,
		PTS:        frame.PTS,
		DTS:        frame.PTS,
		Duration:   frame.Duration,
		FrameType:  ft,
		IsKeyFrame: isKey,
		Size:       len(data),
	}, nil
}

// ErrEncoderFlushed is returned when attempting to send frames to a flushed encoder.
var ErrEncoderFlushed = errors.New("encoder: encoder has been flushed")
