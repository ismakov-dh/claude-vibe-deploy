package docker

import (
	"io/fs"
	"os"
	"text/template"
	"time"
)

// ComposeData holds the data for rendering the app compose template.
type ComposeData struct {
	Name          string
	AppType       string
	Port          int
	Routing       string
	Domain        string
	HasEnvFile    bool
	NeedsDB       bool
	Timestamp     string
}

// GenerateComposeFile renders the app compose template and writes it to disk.
func GenerateComposeFile(tmplFS fs.FS, data ComposeData, destPath string) error {
	tmplContent, err := fs.ReadFile(tmplFS, "templates/compose/app.yml.tmpl")
	if err != nil {
		return err
	}

	t, err := template.New("compose").Parse(string(tmplContent))
	if err != nil {
		return err
	}

	data.Timestamp = time.Now().UTC().Format(time.RFC3339)

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, data)
}
