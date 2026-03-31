package app

// AppType represents a detected application type.
type AppType string

const (
	StaticPlain   AppType = "static-plain"
	StaticBuild   AppType = "static-build"
	NodeServer    AppType = "node-server"
	NodeNext      AppType = "node-next"
	PythonFlask   AppType = "python-flask"
	PythonFastAPI AppType = "python-fastapi"
	PythonDjango  AppType = "python-django"
	PythonGeneric AppType = "python-generic"
	GoApp         AppType = "go"
	Custom        AppType = "custom"
)

// DockerfileTemplate returns the embedded template path for this app type.
func (t AppType) DockerfileTemplate() string {
	m := map[AppType]string{
		StaticPlain:   "templates/dockerfiles/static-plain.Dockerfile",
		StaticBuild:   "templates/dockerfiles/static-build.Dockerfile",
		NodeServer:    "templates/dockerfiles/node-server.Dockerfile",
		NodeNext:      "templates/dockerfiles/node-next.Dockerfile",
		PythonFlask:   "templates/dockerfiles/python-web.Dockerfile",
		PythonFastAPI: "templates/dockerfiles/python-web.Dockerfile",
		PythonDjango:  "templates/dockerfiles/python-django.Dockerfile",
		PythonGeneric: "templates/dockerfiles/python-web.Dockerfile",
		GoApp:         "templates/dockerfiles/go.Dockerfile",
	}
	if p, ok := m[t]; ok {
		return p
	}
	return ""
}

// DefaultPort returns the default port for this app type.
func (t AppType) DefaultPort() int {
	switch t {
	case StaticPlain, StaticBuild:
		return 80
	case PythonFlask, PythonFastAPI, PythonDjango, PythonGeneric:
		return 8000
	case GoApp:
		return 8080
	default:
		return 3000
	}
}

// IsValid returns true if this is a known app type.
func (t AppType) IsValid() bool {
	return t.DockerfileTemplate() != "" || t == Custom
}
