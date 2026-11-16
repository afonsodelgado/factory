package phone

import (
	"strings"

	"rsc.io/qr"
)

const quietZone = 2

func QR(text string) ([]string, error) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return nil, err
	}
	light := func(x, y int) bool {
		inside := x >= 0 && y >= 0 && x < code.Size && y < code.Size
		return !inside || !code.Black(x, y)
	}
	var lines []string
	for y := -quietZone; y < code.Size+quietZone; y += 2 {
		var line strings.Builder
		for x := -quietZone; x < code.Size+quietZone; x++ {
			line.WriteString(halfBlock(light(x, y), light(x, y+1)))
		}
		lines = append(lines, line.String())
	}
	return lines, nil
}

func halfBlock(top, bottom bool) string {
	switch {
	case top && bottom:
		return "█"
	case top:
		return "▀"
	case bottom:
		return "▄"
	}
	return " "
}
