package types

// Plan is a Terraform plan's resource changes and their totals.
type Plan struct {
	// Changes are the resources the plan changes, in plan order; no-op and read
	// actions are left out.
	Changes []ResourceChange `json:"changes"`

	// Add counts resources created, replacements included.
	Add int `json:"add"`

	// Change counts resources updated in place.
	Change int `json:"change"`

	// Destroy counts resources destroyed, replacements included.
	Destroy int `json:"destroy"`
}

// ResourceChange is one resource a plan changes.
type ResourceChange struct {
	// Address is the resource's full address (module.x.aws_s3_bucket.y).
	Address string `json:"address"`

	// Type is the resource type (aws_s3_bucket).
	Type string `json:"type"`

	// Name is the resource's name in its module.
	Name string `json:"name"`

	// Actions are Terraform's actions (create, update, delete; delete and create
	// together are a replacement).
	Actions []string `json:"actions"`
}

// Empty reports whether the plan changes nothing.
func (p Plan) Empty() bool { return len(p.Changes) == 0 }
