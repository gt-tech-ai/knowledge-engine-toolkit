package chaos

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Chaos Mesh resource vocabulary (chaos-mesh.org/v1alpha1).
const (
	// chaosMeshAPIVersion is the Chaos Mesh CRD group/version.
	chaosMeshAPIVersion = "chaos-mesh.org/v1alpha1"
	// chaosMeshGroup is the CRD API group kubectl qualifies the resource with.
	chaosMeshGroup = "chaos-mesh.org"
	// serviceLabel selects a service's pods: every app chart labels them with it.
	serviceLabel = "app.kubernetes.io/name"
	// managedByLabel marks the resources this injector creates (Config.ManagedBy), so an
	// operator can list or clean up every leftover with one selector.
	managedByLabel = "app.kubernetes.io/managed-by"
)

// chaosMeshInjector injects faults as Chaos Mesh custom resources: Inject creates the
// resource with `kubectl create`, Revert deletes it and waits, which makes the Chaos
// Mesh controller restore the target. Each resource also carries the fault's duration,
// so Chaos Mesh recovers the fault by itself if the runner dies before it reverts.
type chaosMeshInjector struct {
	// runner executes kubectl.
	runner interfaces.CommandRunner

	// root is the working directory kubectl runs in.
	root string

	// managedBy is the managed-by label value.
	managedBy string
}

// newChaosMesh builds the Chaos Mesh injector; it needs a runner.
func newChaosMesh(
	_ context.Context,
	cfg Config,
	deps Deps,
) (interfaces.FaultInjector, error) {
	if deps.Runner == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"chaos chaos-mesh: no command runner",
		)
	}
	managedBy := cfg.ManagedBy
	if managedBy == "" {
		managedBy = DefaultManagedBy
	}
	return &chaosMeshInjector{
		runner:    deps.Runner,
		root:      deps.Root,
		managedBy: managedBy,
	}, nil
}

// chaosMeshResource is a Chaos Mesh custom resource as kubectl reads it (JSON is YAML).
type chaosMeshResource struct {
	// Metadata names and labels the resource in the target namespace.
	Metadata chaosMeshMeta `json:"metadata"`
	// Spec is a podChaosSpec or a networkChaosSpec.
	Spec any `json:"spec"`
	// APIVersion is chaosMeshAPIVersion.
	APIVersion string `json:"apiVersion"`
	// Kind is PodChaos or NetworkChaos.
	Kind string `json:"kind"`
}

// chaosMeshMeta is the resource metadata.
type chaosMeshMeta struct {
	// Labels carries the managed-by marker.
	Labels map[string]string `json:"labels"`
	// Name is the per-run fault name.
	Name string `json:"name"`
	// Namespace is the target namespace.
	Namespace string `json:"namespace"`
}

// chaosMeshSelector selects pods by namespace and labels.
type chaosMeshSelector struct {
	// LabelSelectors are the labels every selected pod carries.
	LabelSelectors map[string]string `json:"labelSelectors"`
	// Namespaces limits the selection to these namespaces.
	Namespaces []string `json:"namespaces"`
}

// podChaosSpec is a PodChaos spec: kill one selected pod.
type podChaosSpec struct {
	// Action is "pod-kill".
	Action string `json:"action"`
	// Mode is "one": a single pod of the selection.
	Mode string `json:"mode"`
	// Duration bounds the experiment on the Chaos Mesh side.
	Duration string `json:"duration"`
	// Selector selects the target service's pods.
	Selector chaosMeshSelector `json:"selector"`
}

// networkChaosSpec is a NetworkChaos spec: degrade traffic from every selected pod to
// every pod of the target selection.
type networkChaosSpec struct {
	// Delay parameterizes a delay action.
	Delay *networkChaosDelay `json:"delay,omitempty"`
	// Loss parameterizes a loss action.
	Loss *networkChaosLoss `json:"loss,omitempty"`
	// Target selects the pods the degraded traffic is headed to.
	Target networkChaosTarget `json:"target"`
	// Action is "delay" or "loss".
	Action string `json:"action"`
	// Mode is "all": every pod of the source selection.
	Mode string `json:"mode"`
	// Direction is "to": only traffic from the source toward the target.
	Direction string `json:"direction"`
	// Duration bounds the experiment on the Chaos Mesh side.
	Duration string `json:"duration"`
	// Selector selects the source service's pods.
	Selector chaosMeshSelector `json:"selector"`
}

// networkChaosTarget is the destination side of a NetworkChaos.
type networkChaosTarget struct {
	// Mode is "all".
	Mode string `json:"mode"`
	// Selector selects the destination service's pods.
	Selector chaosMeshSelector `json:"selector"`
}

// networkChaosDelay is the delay action's parameters.
type networkChaosDelay struct {
	// Latency is the added per-packet delay (a Go duration string, e.g. "300ms").
	Latency string `json:"latency"`
}

// networkChaosLoss is the loss action's parameters.
type networkChaosLoss struct {
	// Loss is the drop probability in percent ("0"-"100").
	Loss string `json:"loss"`
}

// Inject renders f as a Chaos Mesh resource and creates it with kubectl. A resource
// that already exists is another run's: it fails with CodeConflict.
func (c *chaosMeshInjector) Inject(ctx context.Context, f types.Fault) error {
	res, err := chaosMeshResourceFor(f, c.managedBy)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "render chaos resource "+f.Name)
	}
	file, err := os.CreateTemp("", "chaos-*.json")
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "create chaos manifest file")
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return apperr.Wrap(err, apperr.CodeInternal, "write chaos manifest file")
	}
	if err := file.Close(); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "close chaos manifest file")
	}
	out := c.runner.RunBuffered(ctx, c.root, "kubectl", "create", "-f", file.Name())
	if out.Err != nil && strings.Contains(string(out.Stderr), "AlreadyExists") {
		return apperr.Wrap(out.Err, apperr.CodeConflict, fmt.Sprintf(
			"%s %s/%s already exists: another run owns it", res.Kind,
			f.Target.Namespace, f.Name,
		))
	}
	if out.Err != nil {
		return apperr.Wrap(out.Err, apperr.CodeUpstream, fmt.Sprintf(
			"kubectl create %s %s/%s: %s",
			res.Kind, f.Target.Namespace, f.Name, strings.TrimSpace(string(out.Stderr)),
		))
	}
	return nil
}

// Revert deletes f's resource and waits for it to be gone (the Chaos Mesh finalizer
// recovers the fault first). A resource that doesn't exist is already reverted.
func (c *chaosMeshInjector) Revert(ctx context.Context, f types.Fault) error {
	kind, err := chaosMeshKind(f.Spec.Type)
	if err != nil {
		return err
	}
	resource := strings.ToLower(kind) + "." + chaosMeshGroup
	out := c.runner.RunBuffered(ctx, c.root, "kubectl", "delete", resource, f.Name,
		"--namespace", f.Target.Namespace, "--ignore-not-found", "--wait=true")
	if out.Err != nil {
		return apperr.Wrap(out.Err, apperr.CodeUpstream, fmt.Sprintf(
			"kubectl delete %s %s/%s: %s",
			resource, f.Target.Namespace, f.Name, strings.TrimSpace(string(out.Stderr)),
		))
	}
	return nil
}

// Active lists the Chaos Mesh resources this injector's owner created (its managed-by
// value) that still exist in namespace, as kubectl's kind/name.
func (c *chaosMeshInjector) Active(
	ctx context.Context,
	namespace string,
) ([]string, error) {
	out := c.runner.RunBuffered(ctx, c.root, "kubectl", "get",
		"podchaos."+chaosMeshGroup+",networkchaos."+chaosMeshGroup,
		"--namespace", namespace, "--selector", managedByLabel+"="+c.managedBy,
		"--output", "name")
	if out.Err != nil {
		return nil, apperr.Wrap(out.Err, apperr.CodeUpstream, fmt.Sprintf(
			"kubectl get the live chaos faults in %s: %s",
			namespace, strings.TrimSpace(string(out.Stderr)),
		))
	}
	return strings.Fields(string(out.Stdout)), nil
}

// chaosMeshKind maps a fault type to the Chaos Mesh kind that implements it.
func chaosMeshKind(t types.FaultType) (string, error) {
	switch t {
	case types.FaultPodKill:
		return "PodChaos", nil
	case types.FaultNetworkLatency, types.FaultNetworkLoss:
		return "NetworkChaos", nil
	default:
		return "", apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"the chaos-mesh injector has no fault type %q", t,
		))
	}
}

// chaosMeshResourceFor renders f as the Chaos Mesh resource that injects it, scoped to
// the target namespace: the source (and, for network faults, the destination) pods are
// selected there by their service label.
func chaosMeshResourceFor(f types.Fault, managedBy string) (chaosMeshResource, error) {
	kind, err := chaosMeshKind(f.Spec.Type)
	if err != nil {
		return chaosMeshResource{}, err
	}
	ns := f.Target.Namespace
	res := chaosMeshResource{
		APIVersion: chaosMeshAPIVersion,
		Kind:       kind,
		Metadata: chaosMeshMeta{
			Name:      f.Name,
			Namespace: ns,
			Labels:    map[string]string{managedByLabel: managedBy},
		},
	}
	source := podsOf(ns, f.Target.Service)
	switch f.Spec.Type {
	case types.FaultPodKill:
		res.Spec = podChaosSpec{
			Action:   "pod-kill",
			Mode:     "one",
			Selector: source,
			Duration: f.Duration.String(),
		}
	case types.FaultNetworkLatency:
		res.Spec = networkSpec(f, source, "delay",
			&networkChaosDelay{Latency: f.Spec.Latency.String()}, nil)
	case types.FaultNetworkLoss:
		res.Spec = networkSpec(f, source, "loss",
			nil, &networkChaosLoss{Loss: strconv.Itoa(f.Spec.LossPercent)})
	}
	return res, nil
}

// networkSpec builds the NetworkChaos spec degrading source's traffic toward the pods
// of f.Spec.Toward in the same namespace.
func networkSpec(
	f types.Fault,
	source chaosMeshSelector,
	action string,
	delay *networkChaosDelay,
	loss *networkChaosLoss,
) networkChaosSpec {
	return networkChaosSpec{
		Action:    action,
		Mode:      "all",
		Selector:  source,
		Direction: "to",
		Target: networkChaosTarget{
			Mode:     "all",
			Selector: podsOf(f.Target.Namespace, f.Spec.Toward),
		},
		Delay:    delay,
		Loss:     loss,
		Duration: f.Duration.String(),
	}
}

// podsOf selects service's pods in namespace ns.
func podsOf(ns, service string) chaosMeshSelector {
	return chaosMeshSelector{
		Namespaces:     []string{ns},
		LabelSelectors: map[string]string{serviceLabel: service},
	}
}
