package ios

import (
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

func TestTypeByKeysIsPerCall(t *testing.T) {
	d := &Device{}
	if !d.mayPaste(driver.TypeOptions{}) {
		t.Fatal("a plain Type must paste")
	}
	if d.mayPaste(driver.TypeOptions{ByKeys: true}) {
		t.Fatal("ByKeys must skip the pasteboard for that call")
	}
	if !d.mayPaste(driver.TypeOptions{}) {
		t.Fatal("ByKeys on one call must not stick")
	}
	d.TypeByKeys(true)
	if d.mayPaste(driver.TypeOptions{}) {
		t.Fatal("the device-wide switch must still skip the pasteboard")
	}
}
