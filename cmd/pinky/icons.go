package main

import (
	"log/slog"
	"path/filepath"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/icons"
)

func iconsHome(dirs api.Dirs) string { return filepath.Join(dirs.Data, "icons") }

func startIcons(dirs api.Dirs, log *slog.Logger) {
	s := icons.Use(iconsHome(dirs), log)
	log.Info("icon detector", "status", s.String(), "id", s.ID)
}
