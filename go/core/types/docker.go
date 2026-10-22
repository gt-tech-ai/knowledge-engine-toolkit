package types

// BuildSpec describes an image build.
type BuildSpec struct {
	// BuildArgs are `--build-arg` values.
	BuildArgs map[string]string `yaml:"build_args" json:"build_args,omitempty"`

	// Context is the build context directory.
	Context string `yaml:"context" json:"context"`

	// Dockerfile is the Dockerfile path; empty uses the context's Dockerfile.
	Dockerfile string `yaml:"dockerfile" json:"dockerfile,omitempty"`

	// Target is the stage to build; empty builds the last.
	Target string `yaml:"target" json:"target,omitempty"`

	// Platform is the target platform (linux/amd64); empty is the host's.
	Platform string `yaml:"platform" json:"platform,omitempty"`

	// Tags name the built image.
	Tags []string `yaml:"tags" json:"tags,omitempty"`
}

// ComposeSpec selects a Compose stack.
type ComposeSpec struct {
	// Project is the Compose project name; empty uses Compose's default.
	Project string `yaml:"project" json:"project,omitempty"`

	// Files are the Compose files, in override order.
	Files []string `yaml:"files" json:"files"`

	// Profiles enable optional services.
	Profiles []string `yaml:"profiles" json:"profiles,omitempty"`

	// Services limits ComposeUp to these services; empty starts all.
	Services []string `yaml:"services" json:"services,omitempty"`

	// Env are extra `KEY=value` variables for interpolation.
	Env []string `yaml:"env" json:"env,omitempty"`

	// RemoveVolumes makes ComposeDown delete the stack's volumes.
	RemoveVolumes bool `yaml:"remove_volumes" json:"remove_volumes,omitempty"`
}
