//go:build !darwin

package icons

import (
	"fmt"

	"github.com/DIYgod/pinky/internal/driver"
)

func openORT(string, string, *manifest) (engine, error) {
	return nil, fmt.Errorf("the icon detector runs on macOS only: %w", driver.ErrUnsupported)
}
