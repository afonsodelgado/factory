package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolutionPrefersExplicitFieldsThenRootOrchestrationAndNeverGuesses(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(".factory/config.yml", "commands:\n  lint: custom-lint\n")
	write("Makefile", "build lint test:\n\techo passed\n")
	write("go.mod", "module example\n")
	write("package.json", `{"scripts":{"build":"vite build","test":"vitest"},"packageManager":"pnpm@10.33.0"}`)
	r, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile.Commands.Build != "make build" || r.Profile.Commands.Lint != "custom-lint" || r.Profile.Commands.Test != "make test" || len(r.Recipe.Unresolved) != 0 {
		t.Fatal(r)
	}
	digest := r.Recipe.Digest
	write("Makefile", "")
	os.Remove(filepath.Join(root, "go.mod"))
	write(".factory/config.yml", "")
	r, err = Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile.Commands.Build != "pnpm run build" || r.Recipe.Digest == digest || len(r.Recipe.Unresolved) != 1 || !strings.HasPrefix(r.Recipe.Unresolved[0], "lint:") {
		t.Fatal(r)
	}
	write("package.json", `{"scripts":{"build":"vite build","test":"vitest"}}`)
	write("pnpm-lock.yaml", "lockfileVersion: 9\n")
	write("package-lock.json", "{}")
	r, err = Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Recipe.Unresolved) != 3 || r.Profile.Commands.Build != "" {
		t.Fatal("ambiguous package manager guessed", r)
	}
}

func TestCIWorkingDirectoryAndIncompleteBrowserRecipeAreExplicit(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0755)
	os.MkdirAll(filepath.Join(root, ".factory"), 0755)
	os.WriteFile(filepath.Join(root, ".factory", "config.yml"), []byte("qa:\n  mode: browser\n"), 0644)
	os.WriteFile(filepath.Join(root, ".github", "workflows", "ci.yml"), []byte("jobs:\n  go:\n    defaults:\n      run:\n        working-directory: server\n    steps:\n      - run: go test ./...\n      - run: make build\n      - run: make lint\n        env:\n          SECRET: '${{ secrets.TOKEN }}'\n"), 0644)
	r, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if r.Recipe.Commands["test"].Directory != filepath.Join(root, "server") || r.Profile.Commands.Lint != "" || len(r.Recipe.Unresolved) != 6 {
		t.Fatal(r)
	}
}

func TestConflictingBrowserEndpointsRemainQuestions(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".factory"), 0755)
	os.WriteFile(filepath.Join(root, ".factory/config.yml"), []byte("qa: {mode: browser, start: serve, seed: 'true', sign_in: 'true'}\n"), 0644)
	os.WriteFile(filepath.Join(root, "playwright.config.ts"), []byte("baseURL: 'http://localhost:3000', webServer: {url: 'http://localhost:3000/health'}"), 0644)
	os.WriteFile(filepath.Join(root, "playwright.config.js"), []byte("baseURL: 'http://localhost:4000', webServer: {url: 'http://localhost:4000/health'}"), 0644)
	r, err := Resolve(root)
	if err != nil || r.Profile.QA.URL != "" || r.Profile.QA.Health != "" {
		t.Fatal(r, err)
	}
	if !strings.Contains(strings.Join(r.Recipe.Unresolved, " "), "qa.url:") {
		t.Fatal(r.Recipe)
	}
}
