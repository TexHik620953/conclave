package playbook

import "embed"

//go:embed builtin/*.yaml
var builtinFS embed.FS

// LoadBuiltin loads the playbooks shipped with conclave-core.
func LoadBuiltin() (*Registry, error) {
	r := NewRegistry()
	if err := r.LoadFS(builtinFS, "builtin"); err != nil {
		return nil, err
	}
	return r, nil
}
