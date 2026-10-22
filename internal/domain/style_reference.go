package domain

// NamedStyleReference is an explicitly selected style library entry. Only its
// compact synthesis crosses the generation boundary; source text and individual
// source reports belong to the library and are never novel context.
type NamedStyleReference struct {
	ID      string                    `json:"id"`
	Name    string                    `json:"name"`
	Profile *SimulationCompactProfile `json:"profile"`
}
