// Package annexb provides a reader for the AV1 Annex B bitstream format.
//
// The Annex B format (AV1 spec Section B.2) wraps OBUs in a hierarchy of
// leb128-delimited units:
//
//	temporal_unit_size (leb128)
//	  frame_unit_size (leb128)
//	    obu_length (leb128)
//	    open_bitstream_unit (obu_length bytes, obu_has_size_field=0)
//	    ...
//	  frame_unit_size (leb128)
//	    ...
//	  ...
//
// Each OBU in Annex B has obu_has_size_field=0, meaning there is no per-OBU
// leb128 size field after the header -- instead the framing is provided by
// the obu_length that precedes each OBU.
package annexb

import (
	"av1go/bitstream"
	"av1go/obu"
	"fmt"
)

// TemporalUnit represents a single temporal unit from an Annex B stream.
// A temporal unit contains one or more frame units.
type TemporalUnit struct {
	Index      int
	Size       uint64
	FrameUnits []FrameUnit
}

// FrameUnit represents a single frame unit within a temporal unit.
// A frame unit contains one or more OBUs.
type FrameUnit struct {
	Size uint64
	OBUs []obu.Unit
}

// Reader parses an Annex B formatted byte slice into temporal units.
type Reader struct {
	data []byte
}

// NewReader creates an Annex B reader over the given data.
func NewReader(data []byte) *Reader {
	return &Reader{data: data}
}

// ReadAll parses all temporal units from the Annex B stream.
// AV1 spec Section B.2: temporal_unit()
func (r *Reader) ReadAll() ([]TemporalUnit, error) {
	var units []TemporalUnit
	pos := 0
	tuIndex := 0

	for pos < len(r.data) {
		tu, n, err := r.readTemporalUnit(r.data[pos:], tuIndex)
		if err != nil {
			return units, fmt.Errorf("annexb: at byte offset %d: %w", pos, err)
		}
		if n == 0 {
			break
		}
		units = append(units, tu)
		pos += n
		tuIndex++
	}
	return units, nil
}

// readTemporalUnit parses a single temporal unit.
// Returns the temporal unit, bytes consumed, and any error.
func (r *Reader) readTemporalUnit(data []byte, index int) (TemporalUnit, int, error) {
	if len(data) == 0 {
		return TemporalUnit{}, 0, nil
	}

	tuSize, tuSizeLen, err := bitstream.ReadLeb128(data)
	if err != nil {
		return TemporalUnit{}, 0, fmt.Errorf("reading temporal_unit_size: %w", err)
	}

	tu := TemporalUnit{
		Index: index,
		Size:  tuSize,
	}

	consumed := tuSizeLen
	if int(tuSize) > len(data)-consumed {
		return TemporalUnit{}, 0, fmt.Errorf("temporal_unit_size %d exceeds available data %d", tuSize, len(data)-consumed)
	}

	tuData := data[consumed : consumed+int(tuSize)]
	tuPos := 0

	for tuPos < len(tuData) {
		fu, n, err := readFrameUnit(tuData[tuPos:])
		if err != nil {
			return tu, 0, fmt.Errorf("in temporal unit %d: %w", index, err)
		}
		if n == 0 {
			break
		}
		tu.FrameUnits = append(tu.FrameUnits, fu)
		tuPos += n
	}

	return tu, consumed + int(tuSize), nil
}

// readFrameUnit parses a single frame unit.
func readFrameUnit(data []byte) (FrameUnit, int, error) {
	if len(data) == 0 {
		return FrameUnit{}, 0, nil
	}

	fuSize, fuSizeLen, err := bitstream.ReadLeb128(data)
	if err != nil {
		return FrameUnit{}, 0, fmt.Errorf("reading frame_unit_size: %w", err)
	}

	fu := FrameUnit{
		Size: fuSize,
	}

	consumed := fuSizeLen
	if int(fuSize) > len(data)-consumed {
		return FrameUnit{}, 0, fmt.Errorf("frame_unit_size %d exceeds available data %d", fuSize, len(data)-consumed)
	}

	fuData := data[consumed : consumed+int(fuSize)]
	fuPos := 0

	for fuPos < len(fuData) {
		obuLen, obuLenSize, err := bitstream.ReadLeb128(fuData[fuPos:])
		if err != nil {
			return fu, 0, fmt.Errorf("reading obu_length: %w", err)
		}
		fuPos += obuLenSize

		if int(obuLen) > len(fuData)-fuPos {
			return fu, 0, fmt.Errorf("obu_length %d exceeds available data %d", obuLen, len(fuData)-fuPos)
		}

		obuData := fuData[fuPos : fuPos+int(obuLen)]

		// In Annex B, OBUs have obu_has_size_field=0. ParseUnit handles this:
		// when has_size=0, remaining bytes after header become the payload.
		u, _, err := obu.ParseUnit(obuData)
		if err != nil {
			return fu, 0, fmt.Errorf("parsing OBU: %w", err)
		}
		fu.OBUs = append(fu.OBUs, u)
		fuPos += int(obuLen)
	}

	return fu, consumed + int(fuSize), nil
}

// AllOBUs returns a flat list of all OBUs from all temporal/frame units.
// This is a convenience for simple iteration.
func AllOBUs(units []TemporalUnit) []obu.Unit {
	var all []obu.Unit
	for _, tu := range units {
		for _, fu := range tu.FrameUnits {
			all = append(all, fu.OBUs...)
		}
	}
	return all
}
