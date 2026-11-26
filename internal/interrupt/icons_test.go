package interrupt

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSkipsIconLines(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "screens", "save-password.txt"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := ParseScreen(string(b))
	if err != nil {
		t.Fatal(err)
	}
	with, err := ParseScreen(string(b) + "\nicon? \"close\" @352,40\nicon? \"search\" @30,40\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(with.Elements) != len(plain.Elements) {
		t.Fatalf("%d elements with icon lines, %d without", len(with.Elements), len(plain.Elements))
	}
	book := builtin(t)
	want, ok := book.Match(plain, typedLease(time.Now()))
	got, found := book.Match(with, typedLease(time.Now()))
	if !ok || !found || got.Name != want.Name {
		t.Fatalf("matched %v %v, want %v %v", got, found, want, ok)
	}
}
