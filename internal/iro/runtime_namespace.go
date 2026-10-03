package iro

import "path/filepath"

// runtimeNamespaceKey is an opaque path grouping key supplied by provider
// wiring as a single safe path component. Path mechanisms do not parse it.
// Equality is neither remote repository equality nor evidence of membership in
// a local Git common directory. It grants no lifecycle authority.
type runtimeNamespaceKey string

func runtimeLogDir(dirs RuntimeDirs, namespace runtimeNamespaceKey, category string) string {
	return filepath.Join(dirs.StateRoot, category, string(namespace))
}
