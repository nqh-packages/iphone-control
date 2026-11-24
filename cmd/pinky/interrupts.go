package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/interrupt"
)

func interruptsDir(dirs api.Dirs) string {
	if home := os.Getenv(api.HomeEnv); home != "" {
		return filepath.Join(home, "interrupts.d")
	}
	return filepath.Join(dirs.Config, "interrupts.d")
}

func loadInterruptBook(dirs api.Dirs, log *slog.Logger) *interrupt.Book {
	files := append(interrupt.UserFiles(interruptsDir(dirs)), interrupt.Builtin()...)
	book, errs := interrupt.Load(files)
	for _, e := range errs {
		log.Warn("interrupt entry skipped", "file", e.File, "line", e.Line, "rule", e.Rule, "fix", e.Fix)
	}
	return book
}
