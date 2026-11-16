// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk_test

import (
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// TestStructuredDateCalendarBeaconCompanion pins the SDK >= 1.11.0 additive behavior:
// with `EnableStructuredDates` on, every canonical date emits the dedicated calendar-beacon
// companion `H(K_user, "cal:"+iso)` at width 588 (`FlagHasDigit|FlagHasConn`) immediately
// before the unchanged ISO pulse (width 284); with the flag off nothing is emitted.
func TestStructuredDateCalendarBeaconCompanion(t *testing.T) {
	const text = "Deploy on 2026/11/8 and review on 2026年12月01日."

	enc := ramesdk.NewBlindEncoder([]byte("calendar-beacon-pin-key"))
	enc.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
	pkt, _ := enc.PackBlindPacket("U_BC", text, 1700000000)

	calH1 := enc.KeyedHash64("cal:2026-11-08")
	calH2 := enc.KeyedHash64("cal:2026-12-01")
	isoH1 := enc.KeyedHash64("2026-11-08")

	sawCal1, sawCal2, sawIso := false, false, false
	calIdx, isoIdx := -1, -1
	for i, s := range pkt.Slots {
		switch s.H {
		case calH1:
			sawCal1 = true
			if calIdx < 0 {
				calIdx = i
			}
			if s.RawBitsX10 != 588 || s.ClassFlags != ramesdk.FlagHasDigit|ramesdk.FlagHasConn {
				t.Fatalf("calendar beacon atom malformed: %+v", s)
			}
		case calH2:
			sawCal2 = true
			if s.RawBitsX10 != 588 || s.ClassFlags != ramesdk.FlagHasDigit|ramesdk.FlagHasConn {
				t.Fatalf("calendar beacon atom malformed: %+v", s)
			}
		case isoH1:
			sawIso = true
			if isoIdx < 0 {
				isoIdx = i
			}
			if s.RawBitsX10 != 284 {
				t.Fatalf("ISO pulse width changed: %+v", s)
			}
		}
	}
	if !sawCal1 || !sawCal2 || !sawIso {
		t.Fatalf("missing companion atoms: cal1=%v cal2=%v iso=%v (slots=%+v)", sawCal1, sawCal2, sawIso, pkt.Slots)
	}
	if isoIdx != calIdx+1 {
		t.Fatalf("expected the calendar beacon immediately before its ISO pulse: calIdx=%d isoIdx=%d", calIdx, isoIdx)
	}

	// Additivity: with structured dates off, none of the date companions may appear.
	off := ramesdk.NewBlindEncoder([]byte("calendar-beacon-pin-key"))
	offPkt, _ := off.PackBlindPacket("U_BC", text, 1700000000)
	for _, s := range offPkt.Slots {
		if s.H == calH1 || s.H == calH2 || s.H == isoH1 {
			t.Fatalf("structured-date companions must stay opt-in, got %+v", s)
		}
	}
}
