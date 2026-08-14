package prompt

import (
	"strings"
	"testing"
)

func TestStringDefaultOnBlank(t *testing.T) {
	p := New(strings.NewReader("\n"))
	got, err := p.String("Name", "home")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "home" {
		t.Errorf("got %q, want %q", got, "home")
	}
}

func TestStringUsesEnteredValue(t *testing.T) {
	p := New(strings.NewReader("work\n"))
	got, err := p.String("Name", "home")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "work" {
		t.Errorf("got %q, want %q", got, "work")
	}
}

func TestStringEOFErrors(t *testing.T) {
	p := New(strings.NewReader(""))
	_, err := p.String("Name", "")
	if err == nil {
		t.Fatal("expected an error on EOF, got nil")
	}
}

func TestIntDefaultAndReprompt(t *testing.T) {
	p := New(strings.NewReader("not-a-number\n4000\n"))
	got, err := p.Int("Port", 3579)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 4000 {
		t.Errorf("got %d, want %d", got, 4000)
	}
}

func TestIntDefaultOnBlank(t *testing.T) {
	p := New(strings.NewReader("\n"))
	got, err := p.Int("Port", 3579)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 3579 {
		t.Errorf("got %d, want %d", got, 3579)
	}
}

func TestYesNoVariants(t *testing.T) {
	cases := []struct {
		input string
		def   bool
		want  bool
	}{
		{"y\n", false, true},
		{"yes\n", false, true},
		{"n\n", true, false},
		{"no\n", true, false},
		{"\n", true, true},
		{"\n", false, false},
		{"bogus\ny\n", false, true},
	}
	for _, c := range cases {
		p := New(strings.NewReader(c.input))
		got, err := p.YesNo("Remote?", c.def)
		if err != nil {
			t.Fatalf("input %q: unexpected error: %v", c.input, err)
		}
		if got != c.want {
			t.Errorf("input %q, def %v: got %v, want %v", c.input, c.def, got, c.want)
		}
	}
}
