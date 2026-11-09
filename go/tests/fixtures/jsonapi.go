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

// extensionFieldNumber is the field number of the option extension.
const extensionFieldNumber = 50001

// fieldTwo is the second field number of a message.
const fieldTwo = 2

// fieldThree is the third field number of a message.
const fieldThree = 3

// fieldFour is the fourth field number of a message.
const fieldFour = 4

// JSONAPIAnnotations builds the protoreflect.FileDescriptor for a hand-authored
// jsonapi/v1/annotations.proto — the three messages (JsonApiService /
// JsonApiResource / JsonApiOperation) and their three extends (FileOptions /
// MessageOptions / MethodOptions, all field 50001) — resolved against the linked
// descriptor.proto. It lets the tests set the jsonapi extension VALUES on
// synthetic options via dynamicpb without depending on the generated stubs.
func JSONAPIAnnotations(tb testing.TB) protoreflect.FileDescriptor {
	tb.Helper()

	str := func(name string, num int32) *descriptorpb.FieldDescriptorProto {
		var fieldDescriptorProto descriptorpb.FieldDescriptorProto

		fieldDescriptorProto.Name = new(name)
		fieldDescriptorProto.Number = new(num)
		fieldDescriptorProto.Label = descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
		fieldDescriptorProto.Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
		fieldDescriptorProto.JsonName = new(name)

		return &fieldDescriptorProto
	}
	ext := func(name, extendee, typeName string) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:           new(name),
			Number:         new(int32(extensionFieldNumber)),
			Label:          descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:           descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName:       new(typeName),
			Extendee:       new(extendee),
			JsonName:       new(name),
			DefaultValue:   nil,
			OneofIndex:     nil,
			Options:        nil,
			Proto3Optional: nil,
		}
	}
	fdp := jSONAPIAnnotationsTable(str, ext)
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	require.NoError(tb, err, "build synthetic jsonapi/v1/annotations.proto")

	return fd
}

// jSONAPIAnnotationsTable builds the table JSONAPIAnnotations runs.
func jSONAPIAnnotationsTable(
	str func(name string, num int32) *descriptorpb.FieldDescriptorProto,
	ext func(name, extendee, typeName string) *descriptorpb.FieldDescriptorProto,
) *descriptorpb.FileDescriptorProto {
	var fileDescriptorProto descriptorpb.FileDescriptorProto

	fileDescriptorProto.Name = new("jsonapi/v1/annotations.proto")
	fileDescriptorProto.Package = new("jsonapi.v1")
	fileDescriptorProto.Syntax = new("proto3")
	fileDescriptorProto.Dependency = []string{"google/protobuf/descriptor.proto"}
	fileDescriptorProto.MessageType = []*descriptorpb.DescriptorProto{
		{Name: new("JsonApiService"), Field: []*descriptorpb.FieldDescriptorProto{
			str("name", 1), str("base_path", fieldTwo),
		}},
		{Name: new("JsonApiResource"), Field: []*descriptorpb.FieldDescriptorProto{
			str("type", 1), str("single_key", fieldTwo),
			str("collection_key", fieldThree), str("pagination_key", fieldFour),
		}},
		{Name: new("JsonApiOperation"), Field: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     new(textAction),
				Number:   new(int32(1)),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(),
				JsonName: new(textAction),
			},
		}},
	}
	fileDescriptorProto.Extension = []*descriptorpb.FieldDescriptorProto{
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
	}

	return &fileDescriptorProto
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
func NewJSONAPISetter(
	tb testing.TB,
	fileDescriptor protoreflect.FileDescriptor,
) JSONAPISetter {
	tb.Helper()

	return JSONAPISetter{
		svcExt: dynamicpb.NewExtensionType(fileDescriptor.Extensions().ByName("service")),
		resExt: dynamicpb.NewExtensionType(
			fileDescriptor.Extensions().ByName("resource"),
		),
		opExt: dynamicpb.NewExtensionType(
			fileDescriptor.Extensions().ByName("operation"),
		),
		svcMsg: fileDescriptor.Messages().ByName("JsonApiService"),
		resMsg: fileDescriptor.Messages().ByName("JsonApiResource"),
		opMsg:  fileDescriptor.Messages().ByName("JsonApiOperation"),
	}
}

// FileOptsWithService returns FileOptions carrying (jsonapi.v1.service) for the testsvc
// service at /api/v1.
func (s *JSONAPISetter) FileOptsWithService() *descriptorpb.FileOptions {
	m := dynamicpb.NewMessage(s.svcMsg)
	m.Set(s.svcMsg.Fields().ByName("name"), protoreflect.ValueOfString("testsvc"))
	m.Set(s.svcMsg.Fields().ByName("base_path"), protoreflect.ValueOfString("/api/v1"))

	opts := new(descriptorpb.FileOptions)
	proto.SetExtension(opts, s.svcExt, m)

	return opts
}

// MsgOptsWithResource returns MessageOptions carrying (jsonapi.v1.resource) with
// the given type/single/collection keys (empty keys are omitted).
func (s *JSONAPISetter) MsgOptsWithResource(
	resType, single, collection string,
) *descriptorpb.MessageOptions {
	message := dynamicpb.NewMessage(s.resMsg)
	message.Set(s.resMsg.Fields().ByName("type"), protoreflect.ValueOfString(resType))

	if single != "" {
		message.Set(
			s.resMsg.Fields().ByName("single_key"),
			protoreflect.ValueOfString(single),
		)
	}

	if collection != "" {
		message.Set(
			s.resMsg.Fields().ByName("collection_key"),
			protoreflect.ValueOfString(collection),
		)
	}

	opts := new(descriptorpb.MessageOptions)
	proto.SetExtension(opts, s.resExt, message)

	return opts
}

// ActionMethodOpts returns MethodOpts plus the (jsonapi.v1.operation){action:true}
// marker.
func (s *JSONAPISetter) ActionMethodOpts(
	verb, path string, additional ...[value]string,
) *descriptorpb.MethodOptions {
	opts := s.MethodOpts(verb, path, additional...)
	m := dynamicpb.NewMessage(s.opMsg)
	m.Set(s.opMsg.Fields().ByName(textAction), protoreflect.ValueOfBool(true))
	proto.SetExtension(opts, s.opExt, m)

	return opts
}

// MethodOpts returns MethodOptions carrying the google.api.http binding (verb + path,
// plus any additional bindings).
func (*JSONAPISetter) MethodOpts(
	verb, path string, additional ...[value]string,
) *descriptorpb.MethodOptions {
	rule := httpRule(verb, path)
	for _, ab := range additional {
		rule.AdditionalBindings = append(rule.AdditionalBindings, httpRule(ab[0], ab[1]))
	}

	opts := new(descriptorpb.MethodOptions)
	proto.SetExtension(opts, annotations.E_Http, rule)

	return opts
}

// httpRule builds a google.api.HttpRule for an HTTP verb + path template. The
// Pattern oneof is set with the matching concrete wrapper (its interface type is
// unexported, so the rule is built here rather than returning the pattern).
func httpRule(verb, path string) *annotations.HttpRule {
	switch verb {
	case "GET":
		return &annotations.HttpRule{
			Pattern:            &annotations.HttpRule_Get{Get: path},
			Selector:           "",
			Body:               "",
			ResponseBody:       "",
			AdditionalBindings: nil,
		}
	case "PUT":
		return &annotations.HttpRule{
			Pattern:            &annotations.HttpRule_Put{Put: path},
			Selector:           "",
			Body:               "",
			ResponseBody:       "",
			AdditionalBindings: nil,
		}
	case "POST":
		return &annotations.HttpRule{
			Pattern:            &annotations.HttpRule_Post{Post: path},
			Selector:           "",
			Body:               "",
			ResponseBody:       "",
			AdditionalBindings: nil,
		}
	case "PATCH":
		return &annotations.HttpRule{
			Pattern:            &annotations.HttpRule_Patch{Patch: path},
			Selector:           "",
			Body:               "",
			ResponseBody:       "",
			AdditionalBindings: nil,
		}
	case "DELETE":
		return &annotations.HttpRule{
			Pattern:            &annotations.HttpRule_Delete{Delete: path},
			Selector:           "",
			Body:               "",
			ResponseBody:       "",
			AdditionalBindings: nil,
		}
	default:
		return new(annotations.HttpRule)
	}
}

// EmptyMessage is a no-field response message (e.g. a delete response) carrying no
// (jsonapi.v1.resource) option.
func EmptyMessage(name string) *descriptorpb.DescriptorProto {
	var descriptorProto descriptorpb.DescriptorProto

	descriptorProto.Name = new(name)

	return &descriptorProto
}

// ResourceMessage is a response message carrying a (jsonapi.v1.resource) option.
// s must not be nil.
func ResourceMessage(
	jsonapiSetter *JSONAPISetter,
	name, resType, single, collection string,
) *descriptorpb.DescriptorProto {
	var descriptorProto descriptorpb.DescriptorProto

	descriptorProto.Name = new(name)
	descriptorProto.Options = jsonapiSetter.MsgOptsWithResource(
		resType,
		single,
		collection,
	)

	return &descriptorProto
}

// Method builds a MethodDescriptorProto wiring input/output messages (by
// unqualified name in package test.v1) and the given http/operation options.
func Method(
	name, input, output string,
	opts *descriptorpb.MethodOptions,
) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:            new(name),
		InputType:       new(".test.v1." + input),
		OutputType:      new(".test.v1." + output),
		Options:         opts,
		ClientStreaming: nil,
		ServerStreaming: nil,
	}
}

// fdsClosure returns the transitive dependency FileDescriptorProtos of roots in
// dependency order (post-order DFS, deduped by path) so the resulting
// FileDescriptorSet is self-contained for protodesc.NewFiles.
func fdsClosure(
	roots ...protoreflect.FileDescriptor,
) []*descriptorpb.FileDescriptorProto {
	seen := map[string]bool{}

	var (
		out   []*descriptorpb.FileDescriptorProto
		visit func(fd protoreflect.FileDescriptor)
	)

	visit = func(fileDescriptor protoreflect.FileDescriptor) {
		if seen[fileDescriptor.Path()] {
			return
		}

		seen[fileDescriptor.Path()] = true

		imps := fileDescriptor.Imports()

		for i := range imps.Len() {
			visit(imps.Get(i).FileDescriptor)
		}

		out = append(out, protodesc.ToFileDescriptorProto(fileDescriptor))
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

	var fileDescriptorProto descriptorpb.FileDescriptorProto

	fileDescriptorProto.Name = new("test/v1/test.proto")
	fileDescriptorProto.Package = new("test.v1")
	fileDescriptorProto.Syntax = new("proto3")
	fileDescriptorProto.Dependency = []string{
		"google/api/annotations.proto",
		"jsonapi/v1/annotations.proto",
	}
	fileDescriptorProto.Options = fileOpts
	fileDescriptorProto.MessageType = messages
	fileDescriptorProto.Service = []*descriptorpb.ServiceDescriptorProto{{
		Name:   new("TestService"),
		Method: methods,
	}}

	svcFile := &fileDescriptorProto
	fds := &descriptorpb.FileDescriptorSet{File: append(deps, svcFile)}
	raw, err := proto.Marshal(fds)
	require.NoError(tb, err, "marshal synthetic FileDescriptorSet")

	return raw
}

const (
	// value is the value the tests use.
	value = 2
)
