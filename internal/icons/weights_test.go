package icons

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var modelExtensions = []string{".onnx", ".mlmodel", ".mlmodelc", ".mlpackage", ".pt", ".safetensors"}

func weightsIn(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() && p != root && slices.Contains([]string{".git", ".worktrees", ".claude", "build", "vendor", "node_modules", "testdata"}, d.Name()) {
			return filepath.SkipDir
		}
		if slices.Contains(modelExtensions, strings.ToLower(filepath.Ext(p))) {
			found = append(found, rel+": a model file")
		}
		if d.IsDir() || filepath.Ext(p) != ".go" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for line := range strings.SplitSeq(string(b), "\n") {
			patterns, ok := strings.CutPrefix(line, "//go:embed ")
			if ok && slices.ContainsFunc(modelExtensions, func(e string) bool { return strings.Contains(strings.ToLower(patterns), e) }) {
				found = append(found, rel+": embeds a model")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestNoWeightsInRepo(t *testing.T) {
	for _, f := range weightsIn(t, "../..") {
		t.Errorf("%s\n  fix: weights are fetched by `pinkyctl phone icons install` into the data folder, never committed or embedded", f)
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "m.onnx"), nil)
	write(t, filepath.Join(dir, "b.go"), []byte("package b\n\n//go:embed weights/m.PT\nvar b []byte\n"))
	write(t, filepath.Join(dir, "build", "skipped.onnx"), nil)
	if got := weightsIn(t, dir); len(got) != 2 {
		t.Fatalf("the check found %q in its fixture", got)
	}
}
