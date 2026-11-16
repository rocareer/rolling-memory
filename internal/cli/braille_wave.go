// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"fmt"
	"strings"
)

// ============================================================================
// Terminal High-Resolution Unicode Braille Oscilloscope Renderer (`braille_wave.go`)
//
// Each Unicode Braille character (`U+2800` .. `U+28FF`) encodes a 2×4 sub-pixel matrix:
//   (dx=0, dy=0) -> 0x01    (dx=1, dy=0) -> 0x08
//   (dx=0, dy=1) -> 0x02    (dx=1, dy=1) -> 0x10
//   (dx=0, dy=2) -> 0x04    (dx=1, dy=2) -> 0x20
//   (dx=0, dy=3) -> 0x40    (dx=1, dy=3) -> 0x80
//
// A 4-row × 44-column character grid provides an 88×16 sub-pixel oscilloscope canvas,
// rendering continuous smooth-interpolated wave envelopes directly in the terminal.
// ============================================================================

var brailleDotBits = [2][4]uint8{
	{0x01, 0x02, 0x04, 0x40}, // left sub-column (dx = 0)
	{0x08, 0x10, 0x20, 0x80}, // right sub-column (dx = 1)
}

// WaveTerminalSlot holds one token's carrier amplitude and hex nibble for CLI display.
type WaveTerminalSlot struct {
	Token string  `json:"token"`
	Amp   float64 `json:"amp"`
	Hex   string  `json:"hex"`
}

// ComputeTerminalWaveSpec extracts surface tokens from `text` and computes normalized
// carrier amplitudes in [0.14, 1.00] along with the 1-char-per-token hexadecimal code.
func ComputeTerminalWaveSpec(text string) (amps []float64, hexCode string, slots []WaveTerminalSlot) {
	const hexChars = "0123456789ABCDEF"
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case ' ', '\n', '\r', '\t', ',', '.', ';', ':', '!', '?', '|', '(', ')', '[', ']', '{', '}', '"', '\'':
			return true
		default:
			return false
		}
	})
	var tokens []string
	for _, f := range fields {
		t := strings.TrimSpace(f)
		if t != "" {
			tokens = append(tokens, t)
			if len(tokens) >= 24 {
				break
			}
		}
	}
	if len(tokens) == 0 {
		return []float64{0.20, 0.55, 0.90, 0.35, 0.78, 0.25}, "38E5C4", []WaveTerminalSlot{
			{Token: "idle", Amp: 0.20, Hex: "3"},
		}
	}

	maxW := 1.0
	rawW := make([]float64, len(tokens))
	for i, tok := range tokens {
		var w float64
		for _, r := range tok {
			if r >= 0x4E00 && r <= 0x9FFF {
				w += 2.6
			} else if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				w += 1.6
			} else {
				w += 1.0
			}
		}
		rawW[i] = w
		if w > maxW {
			maxW = w
		}
	}

	amps = make([]float64, len(tokens))
	slots = make([]WaveTerminalSlot, len(tokens))
	var hexSB strings.Builder
	for i, w := range rawW {
		norm := w / maxW
		if norm < 0.14 {
			norm = 0.14
		} else if norm > 1.0 {
			norm = 1.0
		}
		norm = float64(int(norm*100+0.5)) / 100.0
		hIdx := int(norm * 15.99)
		if hIdx < 0 {
			hIdx = 0
		} else if hIdx > 15 {
			hIdx = 15
		}
		hStr := string(hexChars[hIdx])
		amps[i] = norm
		hexSB.WriteString(hStr)
		slots[i] = WaveTerminalSlot{
			Token: tokens[i],
			Amp:   norm,
			Hex:   hStr,
		}
	}
	return amps, hexSB.String(), slots
}

// smoothStepCubic performs Hermite smoothstep interpolation $3t^2 - 2t^3$ for $t \in [0, 1]$.
func smoothStepCubic(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * (3.0 - 2.0*t)
}

// RenderBrailleOscilloscope renders a continuous smooth-spline wave envelope using Unicode
// Braille characters (`U+2800..U+28FF`) across `charCols` columns and `charRows` rows.
func RenderBrailleOscilloscope(title string, amps []float64, hexCode string, slots []WaveTerminalSlot, charCols int) string {
	if charCols < 20 {
		charCols = 40
	} else if charCols > 80 {
		charCols = 80
	}
	const charRows = 4
	subW := charCols * 2
	subH := charRows * 4 // 16 vertical sub-pixels (0 = top, 15 = bottom)
	centerY := float64(subH)/2.0 - 0.5

	if len(amps) == 0 {
		amps = []float64{0.2, 0.7, 0.4, 0.9, 0.3}
	}
	// Pad single sample so spline has a span
	samples := amps
	if len(samples) == 1 {
		samples = []float64{0.25, samples[0], 0.25}
	}

	grid := make([][]uint8, charRows)
	for r := 0; r < charRows; r++ {
		grid[r] = make([]uint8, charCols)
	}

	setDot := func(sx, sy int) {
		if sx < 0 || sx >= subW || sy < 0 || sy >= subH {
			return
		}
		cCol := sx / 2
		dx := sx % 2
		cRow := sy / 4
		dy := sy % 4
		grid[cRow][cCol] |= brailleDotBits[dx][dy]
	}

	drawLine := func(x0, y0, x1, y1 int) {
		dx := x1 - x0
		if dx < 0 {
			dx = -dx
		}
		dy := y1 - y0
		if dy < 0 {
			dy = -dy
		}
		sx := 1
		if x0 >= x1 {
			sx = -1
		}
		sy := 1
		if y0 >= y1 {
			sy = -1
		}
		err := dx - dy
		for {
			setDot(x0, y0)
			if x0 == x1 && y0 == y1 {
				break
			}
			e2 := 2 * err
			if e2 > -dy {
				err -= dy
				x0 += sx
			}
			if e2 < dx {
				err += dx
				y0 += sy
			}
		}
	}

	// Dotted center zero-phase axis
	midSubY := int(centerY + 0.5)
	for sx := 0; sx < subW; sx += 2 {
		setDot(sx, midSubY)
	}

	// Sample continuous cubic-Hermite spline across all `subW` horizontal sub-columns
	n := len(samples)
	prevTopY := -1
	prevBotY := -1
	for sx := 0; sx < subW; sx++ {
		pos := float64(sx) / float64(subW-1) * float64(n-1)
		idx := int(pos)
		if idx >= n-1 {
			idx = n - 2
		}
		frac := smoothStepCubic(pos - float64(idx))
		amp := samples[idx]*(1.0-frac) + samples[idx+1]*frac

		topY := int(centerY - amp*7.2 + 0.5)
		botY := int(centerY + amp*5.2 + 0.5)
		if topY < 0 {
			topY = 0
		}
		if botY >= subH {
			botY = subH - 1
		}

		if prevTopY >= 0 {
			drawLine(sx-1, prevTopY, sx, topY)
			drawLine(sx-1, prevBotY, sx, botY)
		} else {
			setDot(sx, topY)
			setDot(sx, botY)
		}
		// Subtle vertical energy fill at high-amplitude peaks
		if amp >= 0.76 && sx%2 == 0 {
			for fy := topY + 1; fy < botY; fy += 2 {
				setDot(sx, fy)
			}
		}
		prevTopY = topY
		prevBotY = botY
	}

	var sb strings.Builder
	borderBar := strings.Repeat("─", charCols)
	sb.WriteString(fmt.Sprintf("╭─ %s [Hex: 0x%s · %d slots] \n", title, hexCode, len(slots)))
	sb.WriteString("├" + borderBar + "╮\n")
	for r := 0; r < charRows; r++ {
		sb.WriteString("│")
		for c := 0; c < charCols; c++ {
			ch := rune(0x2800) + rune(grid[r][c])
			sb.WriteRune(ch)
		}
		sb.WriteString("│\n")
	}
	sb.WriteString("╰" + borderBar + "╯\n")

	if len(slots) > 0 {
		var peakParts []string
		for _, sl := range slots {
			if sl.Amp >= 0.75 || len(slots) <= 6 {
				peakParts = append(peakParts, fmt.Sprintf("%s[%s]", sl.Token, sl.Hex))
			}
		}
		if len(peakParts) > 0 {
			sb.WriteString("  Peaks: " + strings.Join(peakParts, " · ") + "\n")
		}
	}
	return sb.String()
}
