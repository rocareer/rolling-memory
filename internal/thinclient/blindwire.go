// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"fmt"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// Blind-pulse payload codec — delegated 100% to the canonical SDK (`sdk/go/ramesdk.go`).
// No duplicate RMBP wire codec implementation is maintained in `internal/thinclient`.

// EncodePulsePayload flattens the packet's atoms and sentence frames into the canonical `RMBP` base64 payload string via ramesdk.
func EncodePulsePayload(pkt BlindPulsePacket) (string, error) {
	return ramesdk.EncodeWirePayload(pkt.Slots, pkt.SentenceFrames)
}

// DecodePulsePayloadB64 is the inverse of EncodePulsePayload, delegating to ramesdk.DecodeWirePayload.
func DecodePulsePayloadB64(s string) ([]BlindTokenAtom, []int, error) {
	atoms, frames, err := ramesdk.DecodeWirePayload(s)
	if err != nil {
		return nil, nil, fmt.Errorf("thinclient: %w", err)
	}
	var sent []int
	if len(frames) > 0 {
		sent = make([]int, 0, len(frames))
		cum := 0
		for _, fr := range frames {
			cum += len(fr)
			sent = append(sent, cum)
		}
	}
	return atoms, sent, nil
}
