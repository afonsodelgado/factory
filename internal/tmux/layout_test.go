package tmux

import "testing"

func TestTmux38JSONLayoutReadsLikeTheClassicString(t *testing.T) {
	classic := "bb62,80x24,0,0{40x24,0,0,1,39x24,41,0[39x12,41,0,2,39x11,41,13,3]}"
	fromJSON := `{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p","w":40,"h":24,"x":0,"y":0,"a":true,"i":0,"I":"%1"},{"t":"v","w":39,"h":24,"x":41,"y":0,"c":[{"t":"p","w":39,"h":12,"x":41,"y":0,"i":1,"I":"%2"},{"t":"p","w":39,"h":11,"x":41,"y":13,"i":2,"I":"%3"}]}]}}`
	want, err := ParseLayout(classic)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseLayout(fromJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Errorf("tmux 3.8 layout read as %s, want %s: the hub would lose track of the slot's splits", got, want)
	}
}
