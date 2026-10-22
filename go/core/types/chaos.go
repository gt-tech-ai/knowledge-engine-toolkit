package types

import "time"

// FaultType names a chaos fault. The set is closed: every injector Kind implements all
// of them.
type FaultType string

const (
	// FaultPodKill deletes one pod of the target service; its controller replaces it.
	FaultPodKill FaultType = "pod-kill"

	// FaultNetworkLatency delays every packet from the target service's pods toward the
	// pods of FaultSpec.Toward by FaultSpec.Latency.
	FaultNetworkLatency FaultType = "network-latency"

	// FaultNetworkLoss drops FaultSpec.LossPercent of the packets from the target
	// service's pods toward the pods of FaultSpec.Toward.
	FaultNetworkLoss FaultType = "network-loss"
)

// FaultTarget is where a fault lands.
type FaultTarget struct {
	// Namespace is the Kubernetes namespace of the target pods.
	Namespace string `yaml:"namespace" json:"namespace"`

	// Service selects the target pods by their app.kubernetes.io/name label.
	Service string `yaml:"service" json:"service"`
}

// FaultSpec is a fault's type and parameters.
type FaultSpec struct {
	// Type is the fault type.
	Type FaultType `yaml:"type" json:"type"`

	// Toward names the service (app.kubernetes.io/name, same namespace) whose pods a
	// network fault's traffic is headed to. Required for network faults only.
	Toward string `yaml:"toward,omitempty" json:"toward,omitempty"`

	// Latency is the delay a network-latency fault adds to each packet.
	Latency time.Duration `yaml:"latency,omitempty" json:"latency,omitempty"`

	// LossPercent is the share of packets (1-100) a network-loss fault drops.
	LossPercent int `yaml:"loss_percent,omitempty" json:"loss_percent,omitempty"`
}

// Fault is one fault resolved for a single run.
type Fault struct {
	// Name names the resource the injector creates; it carries the run id, so a run
	// never collides with a leftover from an earlier one.
	Name string

	// Target is where the fault lands.
	Target FaultTarget

	// Spec is the fault type and its parameters.
	Spec FaultSpec

	// Duration is how long the fault stays injected. An injector that can bound a
	// fault's lifetime itself sets it too, so the fault expires even if the runner dies.
	Duration time.Duration
}
