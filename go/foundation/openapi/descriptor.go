package openapi

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// BuildDescriptorSet runs `buf build -o -` in protoDir and returns the marshaled
// FileDescriptorSet buf writes to stdout. `buf build` does not run gen managed mode, so
// the custom jsonapi.v1 options survive — the descriptor is what both the route-table
// generator (BuildRouteTable) and the completeness gate (CheckCompleteness) classify
// over. The bytes never touch disk.
func BuildDescriptorSet(
	ctx context.Context,
	runner interfaces.CommandRunner,
	protoDir string,
) ([]byte, error) {
	res := runner.RunBuffered(ctx, protoDir, "buf", "build", "-o", "-")
	if res.Err != nil {
		return nil, apperr.Wrap(res.Err, apperr.CodeInternal,
			"buf build descriptor: "+strings.TrimSpace(string(res.Stderr)))
	}
	return res.Stdout, nil
}

// Full names of the JSON:API annotation extensions declared in
// contracts/proto/jsonapi/v1/annotations.proto. These are the converter's only
// hard-coded contract knowledge — the generic annotation vocabulary, not any
// per-service API data.
const (
	// jsonAPIServiceExtension is the full name of the file-level
	// jsonapi.v1.service option, read from readServiceOption.
	jsonAPIServiceExtension = "jsonapi.v1.service"
	// jsonAPIResourceExtension is the full name of the message-level
	// jsonapi.v1.resource option, read from readResourceOption.
	jsonAPIResourceExtension = "jsonapi.v1.resource"
	// jsonAPIOperationExtension is the full name of the method-level
	// jsonapi.v1.operation option, read from readOperationOption.
	jsonAPIOperationExtension = "jsonapi.v1.operation"
)

// loadDescriptorTypes unmarshals a marshaled FileDescriptorSet, builds its file
// registry, and returns that registry alongside a Types resolver populated with
// every file's extension definitions (resolved dynamically via dynamicpb). It is
// the shared front half of readServiceConfigs and BuildRouteTable: both need the
// jsonapi.v1 extensions resolved from the descriptor set's OWN definitions so the
// converter never links the generated Go stubs and stays repo-agnostic.
func loadDescriptorTypes(
	fdsBytes []byte,
) (*protoregistry.Files, *protoregistry.Types, error) {
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(fdsBytes, &fds); err != nil {
		return nil, nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"unmarshal descriptor set",
		)
	}

	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		return nil, nil, apperr.Wrap(err, apperr.CodeInternal, "build file registry")
	}

	types := &protoregistry.Types{}
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		exts := fd.Extensions()
		for i := range exts.Len() {
			_ = types.RegisterExtension(dynamicpb.NewExtensionType(exts.Get(i)))
		}
		return true
	})
	return files, types, nil
}

// readServiceConfigs derives the per-service JSON:API config from the
// jsonapi.v1 annotations carried in a marshaled FileDescriptorSet (produced by
// `buf build -o <file>`). It is the single-source-of-truth replacement for the
// hand-maintained knownServices map: files are grouped by the file option's
// service name (so several proto packages can merge into one service), and each
// resource's single/collection/pagination keys are merged by resource type
// across the response messages that declare them.
//
// Extensions are resolved dynamically from the descriptor set's own
// definitions, so the converter never links the generated Go stubs and stays
// repo-agnostic — it reads whatever descriptor set it is given.
//
// It returns the per-service configs (keyed by service name) and a map from
// each proto package path (e.g. "library/v1") to its service name, which the
// converter uses to route each generated swagger file to its service.
func readServiceConfigs(
	fdsBytes []byte,
) (configs map[string]serviceConfig, pkgToService map[string]string, err error) {
	files, types, err := loadDescriptorTypes(fdsBytes)
	if err != nil {
		return nil, nil, err
	}

	svcExt, _ := types.FindExtensionByName(jsonAPIServiceExtension)
	resExt, _ := types.FindExtensionByName(jsonAPIResourceExtension)
	if svcExt == nil || resExt == nil {
		return nil, nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"descriptor set is missing the %s / %s extension definitions",
				jsonAPIServiceExtension, jsonAPIResourceExtension,
			),
		)
	}

	configs = map[string]serviceConfig{}
	pkgToService = map[string]string{}
	var walkErr error

	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		svc, ok, err := readServiceOption(fd, svcExt, types)
		if err != nil {
			walkErr = apperr.Wrap(err, apperr.CodeInternal, fd.Path())
			return false
		}
		if !ok {
			return true // file is not part of the JSON:API surface
		}

		pkgPath := path.Dir(fd.Path())
		if existing, seen := pkgToService[pkgPath]; seen && existing != svc.name {
			walkErr = apperr.New(
				apperr.CodeInvalidInput,
				fmt.Sprintf(
					"package %q maps to conflicting services %q and %q",
					pkgPath, existing, svc.name,
				),
			)
			return false
		}
		pkgToService[pkgPath] = svc.name

		cfg, exists := configs[svc.name]
		if !exists {
			cfg = serviceConfig{
				service:   svc.name,
				basePath:  svc.basePath,
				resources: map[string]resourceConfig{},
			}
		} else if cfg.basePath != svc.basePath {
			// Every file mapped to a service must agree on its base path;
			// otherwise the merged spec's server URL would depend on file order.
			walkErr = apperr.New(
				apperr.CodeInvalidInput,
				fmt.Sprintf(
					"service %q declares conflicting base paths %q and %q",
					svc.name, cfg.basePath, svc.basePath,
				),
			)
			return false
		}

		if err := mergeFileResources(
			fd,
			resExt,
			types,
			svc.name,
			cfg.resources,
		); err != nil {
			walkErr = err
			return false
		}

		configs[svc.name] = cfg
		return true
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}

	// Validate that, within each service, no two resources share a single or
	// collection key. Response matching is by property name, so a shared key
	// would make a response match more than one resource — an ambiguity the
	// (now deterministic) converter would resolve silently. Failing here surfaces
	// it loudly at config-build time.
	for svcName, cfg := range configs {
		if err := validateResourceKeys(svcName, cfg.resources); err != nil {
			return nil, nil, err
		}
	}

	return configs, pkgToService, nil
}

// validateResourceKeys errors if two resources in a service claim the same
// non-empty single_key or collection_key (which would make property-based
// response matching ambiguous).
func validateResourceKeys(svcName string, resources map[string]resourceConfig) error {
	owner := map[string]string{} // key (JSON name) → resource type that claims it
	claim := func(key, resourceType string) error {
		if key == "" {
			return nil
		}
		if prev, taken := owner[key]; taken && prev != resourceType {
			return apperr.New(
				apperr.CodeInvalidInput,
				fmt.Sprintf(
					"service %q: resources %q and %q both claim key %q",
					svcName, prev, resourceType, key,
				),
			)
		}
		owner[key] = resourceType
		return nil
	}
	// Iterate in a stable order so the reported conflict is deterministic.
	types := make([]string, 0, len(resources))
	for t := range resources {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		rc := resources[t]
		if err := claim(rc.singleKey, rc.resourceType); err != nil {
			return err
		}
		if err := claim(rc.collectionKey, rc.resourceType); err != nil {
			return err
		}
	}
	return nil
}

// ResourceKeys is the exported, output-affecting identity of a decoded JSON:API
// resource — the response-property names the converter reads when wrapping a
// response. paginationKey is omitted because it is inert (the collection
// envelope's shape is fixed). It is the black-box surface over the unexported
// resourceConfig for ReadServiceConfigs and ValidateResourceKeys.
type ResourceKeys struct {
	// Type is the JSON:API data.type value (e.g. "users").
	Type string

	// SingleKey is the response property holding one resource (e.g. "user");
	// empty for collection-only resources.
	SingleKey string

	// CollectionKey is the response property holding a resource list (e.g.
	// "users"); empty for single-only resources.
	CollectionKey string
}

// ServiceView is the exported projection of a service's decoded JSON:API config:
// its REST base path and the per-resource keys the jsonapi.v1 annotations encode,
// keyed by resource type. ReadServiceConfigs returns it so callers (and black-box
// tests) can inspect the decoded contract without depending on the unexported
// serviceConfig/resourceConfig structs.
type ServiceView struct {
	// Resources holds the service's decoded JSON:API resource keys, keyed by
	// resource type.
	Resources map[string]ResourceKeys
	// BasePath is the service's REST base path (e.g. "/v1/library").
	BasePath string
}

// serviceOpt holds the decoded jsonapi.v1.service file option.
type serviceOpt struct {
	// name is the JSON:API service name (e.g. "library"), used to group and route
	// swagger files to their merged per-service spec.
	name string
	// basePath is the service's REST base path (e.g. "/v1/library").
	basePath string
}

// mergeFileResources merges every (jsonapi.v1.resource)-annotated message in fd into
// resources (keyed by resource type), failing on a type-less annotation or on keys that
// conflict with the same type declared elsewhere in the service.
func mergeFileResources(
	fd protoreflect.FileDescriptor,
	resExt protoreflect.ExtensionType,
	types *protoregistry.Types,
	svcName string,
	resources map[string]resourceConfig,
) error {
	msgs := fd.Messages()
	for i := range msgs.Len() {
		md := msgs.Get(i)
		rc, ok, err := readResourceOption(md, resExt, types)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, fd.Path())
		}
		if !ok {
			continue
		}
		if rc.resourceType == "" {
			// A resource annotation without a type cannot key the resource
			// set and would silently collide with other type-less ones.
			return apperr.New(
				apperr.CodeInvalidInput,
				fmt.Sprintf(
					"%s: message %q: (jsonapi.v1.resource) is missing required field 'type'",
					fd.Path(),
					md.Name(),
				),
			)
		}
		merged, err := mergeResource(resources[rc.resourceType], rc)
		if err != nil {
			return apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				fmt.Sprintf("service %q", svcName),
			)
		}
		resources[rc.resourceType] = merged
	}
	return nil
}

// readServiceOption decodes the jsonapi.v1.service file option, if present.
func readServiceOption(
	fd protoreflect.FileDescriptor,
	ext protoreflect.ExtensionType,
	types *protoregistry.Types,
) (serviceOpt, bool, error) {
	msg, ok, err := extensionMessage(fd.Options(), ext, types)
	if err != nil || !ok {
		return serviceOpt{}, false, err
	}
	return serviceOpt{
		name:     stringField(msg, "name"),
		basePath: stringField(msg, "base_path"),
	}, true, nil
}

// readResourceOption decodes the jsonapi.v1.resource message option, if present.
func readResourceOption(
	md protoreflect.MessageDescriptor,
	ext protoreflect.ExtensionType,
	types *protoregistry.Types,
) (resourceConfig, bool, error) {
	msg, ok, err := extensionMessage(md.Options(), ext, types)
	if err != nil || !ok {
		return resourceConfig{}, false, err
	}
	return resourceConfig{
		resourceType:  stringField(msg, "type"),
		singleKey:     stringField(msg, "single_key"),
		collectionKey: stringField(msg, "collection_key"),
		paginationKey: stringField(msg, "pagination_key"),
	}, true, nil
}

// readOperationOption decodes the jsonapi.v1.operation METHOD option, if present,
// and returns its `action` marker. Unlike readResourceOption (a MessageOptions
// extension read from the response message), this reads a MethodOptions extension
// from the method's OWN options — reading it from the wrong descriptor would
// silently default every POST to CREATE-or-error instead of ACTION.
func readOperationOption(
	md protoreflect.MethodDescriptor,
	ext protoreflect.ExtensionType,
	types *protoregistry.Types,
) (bool, error) {
	msg, ok, err := extensionMessage(md.Options(), ext, types)
	if err != nil || !ok {
		return false, err
	}
	return boolField(msg, "action"), nil
}

// extensionMessage re-decodes an options message against the descriptor set's
// own type resolver and returns the message-typed extension value if set.
// protodesc.NewFiles leaves extensions that are absent from the global registry
// as unknown fields; re-marshaling preserves them and unmarshaling into a
// dynamic message with the resolver makes them readable. A dynamic message is
// used (not the concrete *descriptorpb type) because the concrete type does not
// surface a dynamically-resolved extension via HasExtension.
func extensionMessage(
	opts protoreflect.ProtoMessage,
	ext protoreflect.ExtensionType,
	types *protoregistry.Types,
) (protoreflect.Message, bool, error) {
	if opts == nil {
		return nil, false, nil
	}
	raw, err := proto.Marshal(opts)
	if err != nil {
		return nil, false, apperr.Wrap(err, apperr.CodeInternal, "marshal options")
	}
	dm := dynamicpb.NewMessage(opts.ProtoReflect().Descriptor())
	if err := (proto.UnmarshalOptions{Resolver: types}).Unmarshal(raw, dm); err != nil {
		return nil, false, apperr.Wrap(err, apperr.CodeInternal, "decode options")
	}
	xd := ext.TypeDescriptor()
	if !dm.Has(xd) {
		return nil, false, nil
	}
	return dm.Get(xd).Message(), true, nil
}

// stringField reads a string field by its proto (snake_case) name; missing
// fields read as "".
func stringField(msg protoreflect.Message, name string) string {
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return ""
	}
	return msg.Get(fd).String()
}

// boolField reads a bool field by its proto (snake_case) name; a missing field
// reads as false.
func boolField(msg protoreflect.Message, name string) bool {
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return false
	}
	return msg.Get(fd).Bool()
}

// mergeResource unions the single/collection/pagination keys of annotations
// that share a resource type (e.g. the GET response declares single_key and the
// LIST response declares collection_key). Conflicting non-empty values for the
// same field are a contract error.
func mergeResource(existing, incoming resourceConfig) (resourceConfig, error) {
	if existing.resourceType == "" {
		return incoming, nil
	}

	single, err := mergeKey(
		"single_key",
		incoming.resourceType,
		existing.singleKey,
		incoming.singleKey,
	)
	if err != nil {
		return resourceConfig{}, err
	}
	collection, err := mergeKey(
		"collection_key",
		incoming.resourceType,
		existing.collectionKey,
		incoming.collectionKey,
	)
	if err != nil {
		return resourceConfig{}, err
	}
	pagination, err := mergeKey(
		"pagination_key",
		incoming.resourceType,
		existing.paginationKey,
		incoming.paginationKey,
	)
	if err != nil {
		return resourceConfig{}, err
	}

	return resourceConfig{
		resourceType:  incoming.resourceType,
		singleKey:     single,
		collectionKey: collection,
		paginationKey: pagination,
	}, nil
}

// mergeKey returns the single non-empty value, erroring on a conflict.
func mergeKey(field, resourceType, a, b string) (string, error) {
	switch {
	case a == "":
		return b, nil
	case b == "", a == b:
		return a, nil
	default:
		return "", apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"resource %q declares conflicting %s: %q vs %q",
				resourceType, field, a, b,
			),
		)
	}
}
