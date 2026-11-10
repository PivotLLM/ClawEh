// ClawEh
// License: MIT

package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// taskListFields are TaskModelsConfig's per-use lists and the types of their
// entries. Outside this package a use's models are read only through
// TaskModelsConfig.Entries (or ModelsFor and the helpers built on it), so
// every consumer sees a use the same way and none switches over the lists.
var taskListFields = []string{
	"TextModels", "ImageModels", "TTSModels", "STTModels", "VisionModels", "ClassifyModels",
	"TaskModelRef", "ImageTaskModel", "TTSTaskModel",
}

// taskListExceptions are the directories (relative to the repository root,
// slash-separated) allowed to name taskListFields in non-test code, each
// with why. Empty: the WebUI API reads the lists through Entries for GET and
// decodes the whole block for PUT without naming a list.
var taskListExceptions = map[string]string{}

// taskListUses returns "file:line: name" for every use of taskListFields in
// src: a selector (x.TextModels, config.TaskModelRef) or a composite literal
// key (TaskModelsConfig{TextModels: ...}).
func taskListUses(fset *token.FileSet, file *ast.File) []string {
	var out []string
	report := func(pos token.Pos, name string) {
		p := fset.Position(pos)
		out = append(out, filepath.ToSlash(p.Filename)+":"+strconv.Itoa(p.Line)+": "+name)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if slices.Contains(taskListFields, x.Sel.Name) {
				report(x.Sel.Pos(), x.Sel.Name)
			}
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok && slices.Contains(taskListFields, id.Name) {
				report(id.Pos(), id.Name)
			}
		}
		return true
	})
	return out
}

// TestTaskModelLists_OnlyReadThroughEntries fails when non-test code outside
// package config names a per-use list of task_models (or an entry type)
// instead of going through Entries/ModelsFor.
func TestTaskModelLists_OnlyReadThroughEntries(t *testing.T) {
	root := ".."
	// Not Go consumers: this package, the manual, the frontend, build output.
	skipDirs := map[string]bool{".git": true, ".claude": true, "config": true, "manual": true, "build": true, "bin": true, "web/frontend": true}
	fset := token.NewFileSet()
	var found []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skipDirs[rel] || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			if _, ok := taskListExceptions[rel]; ok {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		scanned++
		found = append(found, taskListUses(fset, file)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned %d Go files; the walk is not reaching the repository", scanned)
	}
	for _, f := range found {
		t.Errorf("%s: read a use's task models through TaskModelsConfig.Entries or ModelsFor", f)
	}
}

// The scan itself finds a list read, an entry type and a literal key.
func TestTaskModelLists_GuardFindsUses(t *testing.T) {
	src := `package p
func f(c *config.Config) {
	_ = c.TaskModels.STTModels
	_ = []config.TaskModelRef{}
	_ = config.TaskModelsConfig{ImageModels: nil}
	_ = c.TaskModels.ModelsFor(config.TaskUseSTT)
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"p.go:3: STTModels", "p.go:4: TaskModelRef", "p.go:5: ImageModels"}
	if got := taskListUses(fset, file); !slices.Equal(got, want) {
		t.Errorf("taskListUses = %v, want %v", got, want)
	}
}
