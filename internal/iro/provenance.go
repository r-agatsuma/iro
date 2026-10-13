package iro

import "fmt"

const unknownModelIdentity = "(unknown; not exposed by runtime)"

// localWorkerProvenance accepts only iro-selected runtime names and identities
// exposed by a trusted connector. Requested options and worker text are not facts.
func localWorkerProvenance(runtime, model string) string {
	if model == "" {
		model = unknownModelIdentity
	}
	return fmt.Sprintf("runtime: %s\nmodel: %s\n", knownValue(runtime), model)
}
