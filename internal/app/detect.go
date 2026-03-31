package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Detect determines the app type from the source directory contents.
func Detect(srcDir string) (AppType, error) {
	// 1. Explicit .vd-type file
	if data, err := os.ReadFile(filepath.Join(srcDir, ".vd-type")); err == nil {
		t := AppType(strings.TrimSpace(string(data)))
		if t.IsValid() {
			return t, nil
		}
	}

	// 2. Custom Dockerfile
	if fileExists(filepath.Join(srcDir, "Dockerfile")) {
		return Custom, nil
	}

	// 3. Python detection
	if hasPythonProject(srcDir) {
		return detectPython(srcDir), nil
	}

	// 4. Node.js detection
	if fileExists(filepath.Join(srcDir, "package.json")) {
		return detectNode(srcDir), nil
	}

	// 5. Go detection
	if fileExists(filepath.Join(srcDir, "go.mod")) {
		return GoApp, nil
	}

	// 6. Plain static site
	if fileExists(filepath.Join(srcDir, "index.html")) {
		return StaticPlain, nil
	}

	return "", nil
}

func hasPythonProject(dir string) bool {
	return fileExists(filepath.Join(dir, "requirements.txt")) ||
		fileExists(filepath.Join(dir, "pyproject.toml")) ||
		fileExists(filepath.Join(dir, "Pipfile"))
}

func detectPython(dir string) AppType {
	if fileExists(filepath.Join(dir, "manage.py")) {
		return PythonDjango
	}
	// Scan .py files for framework imports
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".py") {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			content := string(data)
			if strings.Contains(content, "fastapi") || strings.Contains(content, "FastAPI") {
				return PythonFastAPI
			}
			if strings.Contains(content, "flask") || strings.Contains(content, "Flask") {
				return PythonFlask
			}
		}
	}
	return PythonGeneric
}

func detectNode(dir string) AppType {
	pkg := readPackageJSON(dir)
	if pkg == nil {
		return NodeServer
	}

	deps := mergeMaps(pkg.Dependencies, pkg.DevDependencies)

	if _, ok := pkg.Dependencies["next"]; ok {
		return NodeNext
	}
	if _, ok := deps["vite"]; ok {
		if fileExists(filepath.Join(dir, "server.js")) || fileExists(filepath.Join(dir, "server.ts")) {
			return NodeServer
		}
		return StaticBuild
	}
	for _, framework := range []string{"express", "fastify", "koa", "hono"} {
		if _, ok := pkg.Dependencies[framework]; ok {
			return NodeServer
		}
	}
	if _, ok := pkg.Scripts["build"]; ok {
		return StaticBuild
	}
	return NodeServer
}

type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Scripts         map[string]string `json:"scripts"`
}

func readPackageJSON(dir string) *packageJSON {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	return &pkg
}

func mergeMaps(a, b map[string]string) map[string]string {
	m := make(map[string]string)
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		m[k] = v
	}
	return m
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
