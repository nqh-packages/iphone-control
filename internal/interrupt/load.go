package interrupt

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed books/*.yaml
var builtinFS embed.FS

const (
	defaultMax    = 2
	capMax        = 3
	defaultWindow = 5 * time.Minute
	capWindow     = 10 * time.Minute
)

type File struct {
	Name    string
	Data    []byte
	Builtin bool
}

type LoadError struct {
	File string
	Line int
	Rule string
	Fix  string
}

func (e LoadError) Error() string {
	return fmt.Sprintf("%s:%d: %s\n  fix: %s", e.File, e.Line, e.Rule, e.Fix)
}

type rawFile struct {
	Entries []yaml.Node `yaml:"entries"`
}

type rawWhen struct {
	Apps   []string `yaml:"apps"`
	Texts  []string `yaml:"texts"`
	Any    []string `yaml:"any"`
	Button string   `yaml:"button"`
	Shape  string   `yaml:"shape"`
}

type rawEntry struct {
	Name     string           `yaml:"name"`
	Kind     string           `yaml:"kind"`
	When     rawWhen          `yaml:"when"`
	Do       []map[string]any `yaml:"do"`
	Expect   string           `yaml:"expect"`
	After    string           `yaml:"after"`
	Window   string           `yaml:"window"`
	Max      int              `yaml:"max"`
	Rejected []string         `yaml:"rejected"`
	Report   string           `yaml:"report"`
}

func Builtin() []File {
	names, _ := builtinFS.ReadDir("books")
	var out []File
	for _, n := range names {
		data, err := builtinFS.ReadFile("books/" + n.Name())
		if err == nil {
			out = append(out, File{Name: "books/" + n.Name(), Data: data, Builtin: true})
		}
	}
	return out
}

func UserFiles(dir string) []File {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	sort.Strings(paths)
	var out []File
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			out = append(out, File{Name: p, Data: data})
		}
	}
	return out
}

func Load(files []File) (*Book, []LoadError) {
	sorted := append([]File(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Builtin != sorted[j].Builtin {
			return !sorted[i].Builtin
		}
		return sorted[i].Name < sorted[j].Name
	})
	var errs []LoadError
	var entries []Entry
	userNames := map[string]string{}
	builtinNames := map[string]bool{}
	for _, f := range sorted {
		var rf rawFile
		if err := yaml.Unmarshal(f.Data, &rf); err != nil {
			errs = append(errs, LoadError{File: f.Name, Line: 1, Rule: "not a valid book: " + err.Error(), Fix: "write `entries:` as a list of mappings"})
			continue
		}
		for i := range rf.Entries {
			n := &rf.Entries[i]
			e, err := buildEntry(f, n)
			if err != nil {
				errs = append(errs, *err)
				continue
			}
			if f.Builtin {
				if builtinNames[e.Name] {
					errs = append(errs, LoadError{File: f.Name, Line: n.Line, Rule: "name " + e.Name + " is used twice", Fix: "give each entry a unique name"})
					continue
				}
				builtinNames[e.Name] = true
				if _, shadowed := userNames[e.Name]; shadowed {
					continue
				}
			} else {
				if prev, dup := userNames[e.Name]; dup {
					errs = append(errs, LoadError{File: f.Name, Line: n.Line, Rule: "name " + e.Name + " is already used in " + prev, Fix: "rename it; only a built-in entry may be replaced by the same name"})
					continue
				}
				userNames[e.Name] = f.Name
			}
			entries = append(entries, *e)
		}
	}
	return &Book{entries: entries}, errs
}

func buildEntry(f File, n *yaml.Node) (*Entry, *LoadError) {
	var r rawEntry
	fail := func(rule, fix string) (*Entry, *LoadError) {
		return nil, &LoadError{File: f.Name, Line: n.Line, Rule: rule, Fix: fix}
	}
	if err := n.Decode(&r); err != nil {
		return fail("entry does not parse: "+err.Error(), "use the keys name, kind, when, do, expect, after, window, max, rejected, report")
	}
	if r.Name == "" {
		return fail("entry has no name", "add `name:`")
	}
	e := &Entry{Name: r.Name, Kind: Kind(r.Kind), Expect: r.Expect, After: Trigger(r.After), Rejected: r.Rejected, Report: r.Report, Source: f.Name,
		When: When{Apps: r.When.Apps, Texts: r.When.Texts, Any: r.When.Any, Button: r.When.Button, Shape: Shape(r.When.Shape)}}
	if len(r.When.Texts)+len(r.When.Any) == 0 && r.When.Shape == "" {
		return fail(r.Name+": when needs texts, any or shape", "a button or app alone would fire on any screen: add the text the prompt shows")
	}
	switch e.When.Shape {
	case "", ShapeCodeShown, ShapeCodeField:
	default:
		return fail(r.Name+": unknown shape "+r.When.Shape, "use code_shown or code_field, or match on texts")
	}
	steps, err := buildSteps(r.Do)
	if err != "" {
		return fail(r.Name+": "+err, "dismiss and consent steps are tap, scroll_until_enabled and wait; a credential step is type_code")
	}
	e.Steps = steps
	e.Max = r.Max
	switch {
	case e.Max == 0:
		e.Max = defaultMax
	case e.Max < 0 || e.Max > capMax:
		return fail(fmt.Sprintf("%s: max %d is out of range", r.Name, e.Max), fmt.Sprintf("use 1 to %d", capMax))
	}
	e.Window = defaultWindow
	if r.Window != "" {
		w, perr := time.ParseDuration(r.Window)
		if perr != nil || w <= 0 || w > capWindow {
			return fail(r.Name+": window "+r.Window+" is out of range", "use a duration up to 10m, such as 5m")
		}
		e.Window = w
	}
	if lerr := checkKind(e, r); lerr != nil {
		lerr.Line = n.Line
		return nil, lerr
	}
	return e, nil
}

func buildSteps(raw []map[string]any) ([]Step, string) {
	var out []Step
	for _, m := range raw {
		if len(m) != 1 {
			return nil, "a step has exactly one key"
		}
		for k, v := range m {
			switch Op(k) {
			case OpTap, OpScrollUntilEnabled:
				label, ok := v.(string)
				if !ok || label == "" {
					return nil, k + " takes the button's label"
				}
				out = append(out, Step{Op: Op(k), Label: label})
			case OpWait:
				d, err := time.ParseDuration(fmt.Sprint(v))
				if err != nil || d <= 0 || d > 5*time.Second {
					return nil, "wait takes a duration up to 5s"
				}
				out = append(out, Step{Op: OpWait, Wait: d})
			case OpTypeCode:
				out = append(out, Step{Op: OpTypeCode})
			default:
				return nil, "step " + k + " is not supported"
			}
		}
	}
	return out, ""
}

func checkKind(e *Entry, r rawEntry) *LoadError {
	bad := func(rule, fix string) *LoadError {
		return &LoadError{File: e.Source, Line: 0, Rule: e.Name + ": " + rule, Fix: fix}
	}
	only := func(ops ...Op) bool {
		for _, s := range e.Steps {
			ok := false
			for _, o := range ops {
				ok = ok || s.Op == o
			}
			if !ok {
				return false
			}
		}
		return true
	}
	switch e.Kind {
	case Dismiss:
		if e.Expect != "" || e.After != "" {
			return bad("a dismiss entry has no expect or after", "make it a consent entry, or remove them")
		}
		if len(e.Steps) == 0 || !only(OpTap, OpScrollUntilEnabled, OpWait) {
			return bad("a dismiss entry only taps, scrolls and waits", "move typing to a credential entry")
		}
	case Consent:
		if e.Expect == "" || e.After != TriggerCredentialTyped {
			return bad("a consent entry needs expect and after: credential_typed", "add `expect: <name>` and `after: credential_typed`")
		}
		if len(e.Steps) == 0 || !only(OpTap) {
			return bad("a consent entry only taps", "keep one tap step")
		}
	case Credential:
		if e.Expect == "" {
			return bad("a credential entry needs expect", "add `expect: <name>`")
		}
		if len(e.Steps) != 1 || e.Steps[0].Op != OpTypeCode {
			return bad("a credential entry has one step, type_code", "use `do: [{type_code: true}]`")
		}
	case Report:
		if len(e.Steps) != 0 || r.Report == "" || !strings.Contains(r.Report, "fix:") {
			return bad("a report entry has no steps and a report with a `fix:`", "add `report: \"what happened. fix: what to do\"`")
		}
	default:
		return bad("kind "+string(e.Kind)+" is unknown", "use dismiss, consent, credential or report")
	}
	if e.Kind != Credential && len(e.Rejected) > 0 {
		return bad("only a credential entry has rejected texts", "remove rejected")
	}
	return nil
}
