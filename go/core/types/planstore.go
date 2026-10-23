package types

// PlanSet names one set of saved plans: one plan file per unit directory under Dir,
// kept under Prefix in Bucket.
type PlanSet struct {
	// Dir is the local directory holding <unit>/<File> per unit, and the manifest.
	Dir string

	// Bucket is the object-store bucket the plans are kept in.
	Bucket string

	// Prefix is the key prefix every plan of the set's owner is kept under; Load
	// refuses a manifest key outside it.
	Prefix string

	// RunID keys one Save's plans: each goes to <Prefix><RunID>/<unit>/<File>.
	RunID string

	// Owner names whose plans these are (a deploy environment); Load refuses a
	// manifest made for another owner.
	Owner string

	// File is each unit's plan file name.
	File string

	// Manifest is the manifest file name in Dir.
	Manifest string
}
