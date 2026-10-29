package fixtures

// JSON:API descriptor fixtures: a synthetic jsonapi.v1 annotations file and builders for
// a one-service FileDescriptorSet carrying its file, message and method options, the
// input foundation/openapi's route table, completeness gate and converter read. Unlike
// the mock factories, these take a testing.TB: a malformed fixture fails the test.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// JSONAPIAnnotations builds the protoreflect.FileDescriptor for a hand-authored
// jsonapi/v1/annotations.proto — the three messages (JsonApiService /
// JsonApiResource / JsonApiOperation) and their three extends (FileOptions /
// MessageOptions / MethodOptions, all field 50001) — resolved against the linked
// descriptor.proto. It lets the tests set the jsonapi extension VALUES on
// synthetic options via dynamicpb without depending on the generated stubs.
func JSONAPIAnnotations(tb testing.TB) protoreflect.FileDescriptor {
	tb.Helper()
	str := func(name string, num int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:     new(name),
			Number:   new(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: new(name),
		}
	}
	ext := func(name, extendee, typeName string) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:     new(name),
			Number:   new(int32(50001)),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: new(typeName),
			Extendee: new(extendee),
			JsonName: new(name),
		}
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       new("jsonapi/v1/annotations.proto"),
		Package:    new("jsonapi.v1"),
		Syntax:     new("proto3"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: new("JsonApiService"), Field: []*descriptorpb.FieldDescriptorProto{
				str("name", 1), str("base_path", 2),
			}},
			{Name: new("JsonApiResource"), Field: []*descriptorpb.FieldDescriptorProto{
				str("type", 1), str("single_key", 2),
				str("collection_key", 3), str("pagination_key", 4),
			}},
			{Name: new("JsonApiOperation"), Field: []*descriptorpb.FieldDescriptorProto{
				{
					Name:     new("action"),
					Number:   new(int32(1)),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(),
					JsonName: new("action"),
				},
			}},
		},
		Extension: []*descriptorpb.FieldDescriptorProto{
			ext("service", ".google.protobuf.FileOptions", ".jsonapi.v1.JsonApiService"),
			ext(
				"resource",
				".google.protobuf.MessageOptions",
				".jsonapi.v1.JsonApiResource",
			),
			ext(
				"operation",
				".google.protobuf.MethodOptions",
				".jsonapi.v1.JsonApiOperation",
			),
		},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	require.NoError(tb, err, "build synthetic jsonapi/v1/annotations.proto")
	return fd
}

// JSONAPISetter carries the dynamic extension types + message descriptors decoded from
// the synthetic jsonapi annotations file, so a test can stamp jsonapi.v1.service /
// .resource / .operation onto descriptorpb options without the generated stubs.
type JSONAPISetter struct {
	svcExt, resExt, opExt protoreflect.ExtensionType
	svcMsg, resMsg, opMsg protoreflect.MessageDescriptor
}

// NewJSONAPISetter decodes the extension + message descriptors from the synthetic
// jsonapi annotations file into a JSONAPISetter.
func NewJSONAPISetter(tb testing.TB, fd protoreflect.FileDescriptor) JSONAPISetter {
	tb.Helper()
	return JSONAPISetter{
		svcExt: dynamicpb.NewExtensionType(fd.Extensions().ByName("service")),
		resExt: dynamicpb.NewExtensionType(fd.Extensions().ByName("resource")),
		opExt:  dynamicpb.NewExtensionType(fd.Extensions().ByName("operation")),
		svcMsg: fd.Messages().ByName("JsonApiService"),
		resMsg: fd.Messages().ByName("JsonApiResource"),
		opMsg:  fd.Messages().ByName("JsonApiOperation"),
	}
}

// FileOptsWithService returns FileOptions carrying (jsonapi.v1.service) for the testsvc
// service at /api/v1.
func (s JSONAPISetter) FileOptsWithService() *descriptorpb.FileOptions {
	m := dynamicpb.NewMessage(s.svcMsg)
	m.Set(s.svcMsg.Fields().ByName("name"), protoreflect.ValueOfString("testsvc"))
	m.Set(s.svcMsg.Fields().ByName("base_path"), protoreflect.ValueOfString("/api/v1"))
	opts := &descriptorpb.FileOptions{}
	proto.SetExtension(opts, s.svcExt, m)
	return opts
}

// MsgOptsWithResource returns MessageOptions carrying (jsonapi.v1.resource) with
// the given type/single/collection keys (empty keys are omitted).
func (s JSONAPISetter) MsgOptsWithResource(
	resType, single, collection string,
) *descriptorpb.MessageOptions {
	m := dynamicpb.NewMessage(s.resMsg)
	m.Set(s.resMsg.Fields().ByName("type"), protoreflect.ValueOfString(resType))
	if single != "" {
		m.Set(s.resMsg.Fields().ByName("single_key"), protoreflect.ValueOfString(single))
	}
	if collection != "" {
		m.Set(
			s.resMsg.Fields().ByName("collection_key"),
			protoreflect.ValueOfString(collection),
		)
	}
	opts := &descriptorpb.MessageOptions{}
	proto.SetExtension(opts, s.resExt, m)
	return opts
}

// MethodOpts returns MethodOptions carrying the google.api.http binding (verb +
// path, plus any additional bindings) and, when action is true, the
// (jsonapi.v1.operation){action:true} marker.
func (s JSONAPISetter) MethodOpts(
	verb, path string, action bool, additional ...[2]string,
) *descriptorpb.MethodOptions {
	rule := httpRule(verb, path)
	for _, ab := range additional {
		rule.AdditionalBindings = append(rule.AdditionalBindings, httpRule(ab[0], ab[1]))
	}
	opts := &descriptorpb.MethodOptions{}
	proto.SetExtension(opts, annotations.E_Http, rule)
	if action {
		m := dynamicpb.NewMessage(s.opMsg)
		m.Set(s.opMsg.Fields().ByName("action"), protoreflect.ValueOfBool(true))
		proto.SetExtension(opts, s.opExt, m)
	}
	return opts
}

// httpRule builds a google.api.HttpRule for an HTTP verb + path template. The
// Pattern oneof is set with the matching concrete wrapper (its interface type is
// unexported, so the rule is built here rather than returning the pattern).
func httpRule(verb, path string) *annotations.HttpRule {
	switch verb {
	case "GET":
		return &annotations.HttpRule{Pattern: &annotations.HttpRule_Get{Get: path}}
	case "PUT":
		return &annotations.HttpRule{Pattern: &annotations.HttpRule_Put{Put: path}}
	case "POST":
		return &annotations.HttpRule{Pattern: &annotations.HttpRule_Post{Post: path}}
	case "PATCH":
		return &annotations.HttpRule{Pattern: &annotations.HttpRule_Patch{Patch: path}}
	case "DELETE":
		return &annotations.HttpRule{Pattern: &annotations.HttpRule_Delete{Delete: path}}
	default:
		return &annotations.HttpRule{}
	}
}

// EmptyMessage is a no-field response message (e.g. a delete response) carrying no
// (jsonapi.v1.resource) option.
func EmptyMessage(name string) *descriptorpb.DescriptorProto {
	return &descriptorpb.DescriptorProto{Name: new(name)}
}

// ResourceMessage is a response message carrying a (jsonapi.v1.resource) option.
func ResourceMessage(
	s JSONAPISetter,
	name, resType, single, collection string,
) *descriptorpb.DescriptorProto {
	return &descriptorpb.DescriptorProto{
		Name:    new(name),
		Options: s.MsgOptsWithResource(resType, single, collection),
	}
}

// Method builds a MethodDescriptorProto wiring input/output messages (by
// unqualified name in package test.v1) and the given http/operation options.
func Method(
	name, input, output string,
	opts *descriptorpb.MethodOptions,
) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:       new(name),
		InputType:  new(".test.v1." + input),
		OutputType: new(".test.v1." + output),
		Options:    opts,
	}
}

// fdsClosure returns the transitive dependency FileDescriptorProtos of roots in
// dependency order (post-order DFS, deduped by path) so the resulting
// FileDescriptorSet is self-contained for protodesc.NewFiles.
func fdsClosure(
	roots ...protoreflect.FileDescriptor,
) []*descriptorpb.FileDescriptorProto {
	seen := map[string]bool{}
	var out []*descriptorpb.FileDescriptorProto
	var visit func(fd protoreflect.FileDescriptor)
	visit = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		imps := fd.Imports()
		for i := range imps.Len() {
			visit(imps.Get(i).FileDescriptor)
		}
		out = append(out, protodesc.ToFileDescriptorProto(fd))
	}
	for _, r := range roots {
		visit(r)
	}
	return out
}

// BuildRouteFDS assembles a marshaled, self-contained FileDescriptorSet from the
// synthetic jsonapi annotations file + google/api + a single test.v1 service
// file (messages + methods), the exact input openapi.BuildRouteTable consumes.
func BuildRouteFDS(
	tb testing.TB,
	fileOpts *descriptorpb.FileOptions,
	messages []*descriptorpb.DescriptorProto,
	methods []*descriptorpb.MethodDescriptorProto,
) []byte {
	tb.Helper()
	jaFD := JSONAPIAnnotations(tb)
	deps := fdsClosure(
		descriptorpb.File_google_protobuf_descriptor_proto,
		annotations.File_google_api_annotations_proto,
		jaFD,
	)
	svcFile := &descriptorpb.FileDescriptorProto{
		Name:    new("test/v1/test.proto"),
		Package: new("test.v1"),
		Syntax:  new("proto3"),
		Dependency: []string{
			"google/api/annotations.proto",
			"jsonapi/v1/annotations.proto",
		},
		Options:     fileOpts,
		MessageType: messages,
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name:   new("TestService"),
			Method: methods,
		}},
	}
	fds := &descriptorpb.FileDescriptorSet{File: append(deps, svcFile)}
	raw, err := proto.Marshal(fds)
	require.NoError(tb, err, "marshal synthetic FileDescriptorSet")
	return raw
}
