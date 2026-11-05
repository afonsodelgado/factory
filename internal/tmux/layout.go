package tmux

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type Cell struct {
	Width, Height, X, Y int
	Pane                string
	Horizontal          bool
	Children            []*Cell
}

func (c *Cell) Leaf() bool {
	return len(c.Children) == 0
}

func (c *Cell) Leaves() []string {
	if c.Leaf() {
		return []string{c.Pane}
	}
	var leaves []string
	for _, child := range c.Children {
		leaves = append(leaves, child.Leaves()...)
	}
	return leaves
}

func (c *Cell) Path(pane string) []*Cell {
	if c.Leaf() {
		if c.Pane == pane {
			return []*Cell{c}
		}
		return nil
	}
	for _, child := range c.Children {
		if path := child.Path(pane); path != nil {
			return append([]*Cell{c}, path...)
		}
	}
	return nil
}

func (c *Cell) Fit(x, y, width, height int) {
	c.X, c.Y, c.Width, c.Height = x, y, width, height
	if c.Leaf() {
		return
	}
	sizes := make([]int, len(c.Children))
	for i, child := range c.Children {
		sizes[i] = child.size(c.Horizontal)
	}
	sizes = scale(sizes, c.size(c.Horizontal)-len(sizes)+1)
	for i, child := range c.Children {
		if c.Horizontal {
			child.Fit(x, y, sizes[i], height)
			x += sizes[i] + 1
		} else {
			child.Fit(x, y, width, sizes[i])
			y += sizes[i] + 1
		}
	}
}

func (c *Cell) size(horizontal bool) int {
	if horizontal {
		return c.Width
	}
	return c.Height
}

func scale(sizes []int, to int) []int {
	from := 0
	for _, size := range sizes {
		from += size
	}
	scaled := make([]int, len(sizes))
	used, largest := 0, 0
	for i, size := range sizes {
		scaled[i] = max(size*to/max(from, 1), 1)
		used += scaled[i]
		if scaled[i] > scaled[largest] {
			largest = i
		}
	}
	scaled[largest] += to - used
	return scaled
}

func (c *Cell) String() string {
	body := c.body()
	return fmt.Sprintf("%04x,%s", checksum(body), body)
}

func (c *Cell) body() string {
	head := fmt.Sprintf("%dx%d,%d,%d", c.Width, c.Height, c.X, c.Y)
	if c.Leaf() {
		return head + "," + strings.TrimPrefix(c.Pane, "%")
	}
	parts := make([]string, len(c.Children))
	for i, child := range c.Children {
		parts[i] = child.body()
	}
	open, close := "[", "]"
	if c.Horizontal {
		open, close = "{", "}"
	}
	return head + open + strings.Join(parts, ",") + close
}

func checksum(layout string) uint16 {
	var sum uint16
	for i := 0; i < len(layout); i++ {
		sum = (sum >> 1) + ((sum & 1) << 15)
		sum += uint16(layout[i])
	}
	return sum
}

func ParseLayout(layout string) (*Cell, error) {
	if strings.HasPrefix(layout, "{") {
		return parseJSONLayout(layout)
	}
	_, body, ok := strings.Cut(layout, ",")
	if !ok {
		return nil, fmt.Errorf("layout %q has no checksum", layout)
	}
	p := &layoutParser{in: body}
	cell, err := p.cell()
	if err == nil && p.at != len(body) {
		err = fmt.Errorf("trailing %q", body[p.at:])
	}
	if err != nil {
		return nil, fmt.Errorf("layout %q: %w", layout, err)
	}
	return cell, nil
}

type jsonCell struct {
	Type     string     `json:"t"`
	Width    int        `json:"w"`
	Height   int        `json:"h"`
	X        int        `json:"x"`
	Y        int        `json:"y"`
	Index    int        `json:"i"`
	Pane     string     `json:"I"`
	Children []jsonCell `json:"c"`
}

func parseJSONLayout(layout string) (*Cell, error) {
	var doc struct {
		Root *jsonCell `json:"L"`
	}
	if err := json.Unmarshal([]byte(layout), &doc); err != nil {
		return nil, fmt.Errorf("layout %q: %w", layout, err)
	}
	if doc.Root == nil {
		return nil, fmt.Errorf("layout %q has no tiled panes", layout)
	}
	return doc.Root.cell(), nil
}

func (j jsonCell) cell() *Cell {
	c := &Cell{Width: j.Width, Height: j.Height, X: j.X, Y: j.Y, Pane: j.Pane, Horizontal: j.Type == "h"}
	for _, child := range j.Children {
		c.Children = append(c.Children, child.cell())
	}
	return c
}

type layoutParser struct {
	in string
	at int
}

func (p *layoutParser) cell() (*Cell, error) {
	c := &Cell{}
	for _, field := range []struct {
		into *int
		then byte
	}{{&c.Width, 'x'}, {&c.Height, ','}, {&c.X, ','}, {&c.Y, 0}} {
		n, err := p.number()
		if err != nil {
			return nil, err
		}
		*field.into = n
		if field.then != 0 {
			if err := p.expect(field.then); err != nil {
				return nil, err
			}
		}
	}
	switch p.peek() {
	case ',':
		p.at++
		id, err := p.number()
		if err != nil {
			return nil, err
		}
		c.Pane = "%" + strconv.Itoa(id)
		return c, nil
	case '{', '[':
		c.Horizontal = p.peek() == '{'
		closing := byte(']')
		if c.Horizontal {
			closing = '}'
		}
		p.at++
		for {
			child, err := p.cell()
			if err != nil {
				return nil, err
			}
			c.Children = append(c.Children, child)
			if p.peek() != ',' {
				break
			}
			p.at++
		}
		return c, p.expect(closing)
	}
	return nil, fmt.Errorf("unexpected %q at %d", p.peek(), p.at)
}

func (p *layoutParser) peek() byte {
	if p.at >= len(p.in) {
		return 0
	}
	return p.in[p.at]
}

func (p *layoutParser) expect(b byte) error {
	if p.peek() != b {
		return fmt.Errorf("want %q at %d, got %q", b, p.at, p.peek())
	}
	p.at++
	return nil
}

func (p *layoutParser) number() (int, error) {
	start := p.at
	for p.at < len(p.in) && p.in[p.at] >= '0' && p.in[p.at] <= '9' {
		p.at++
	}
	return strconv.Atoi(p.in[start:p.at])
}
