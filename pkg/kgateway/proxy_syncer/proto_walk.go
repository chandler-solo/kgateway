package proxy_syncer

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

// walkProtoMessages visits msg and, recursively, every message nested within
// it — list and map elements included — unmarshaling anypb.Any values so
// typed_config extensions are walked too. It is the shared traversal under the
// cluster- and secret-reference scans; scanLabel names the scan in the debug
// log for Any payloads whose Go types are not linked into the binary.
func walkProtoMessages(msg proto.Message, scanLabel string, visit func(proto.Message)) {
	if msg == nil {
		return
	}
	visit(msg)
	walkNestedProtoMessages(msg.ProtoReflect(), scanLabel, visit)
}

func walkNestedProtoMessages(msg protoreflect.Message, scanLabel string, visit func(proto.Message)) {
	if !msg.IsValid() {
		return
	}

	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Message() != nil:
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				walkProtoValue(list.Get(i), scanLabel, visit)
			}
		case fd.IsMap() && fd.MapValue().Message() != nil:
			m := v.Map()
			m.Range(func(_ protoreflect.MapKey, value protoreflect.Value) bool {
				walkProtoValue(value, scanLabel, visit)
				return true
			})
		case !fd.IsList() && !fd.IsMap() && fd.Message() != nil:
			walkProtoValue(v, scanLabel, visit)
		}
		return true
	})
}

func walkProtoValue(v protoreflect.Value, scanLabel string, visit func(proto.Message)) {
	msg := v.Message()
	if !msg.IsValid() {
		return
	}

	if anyMsg, ok := msg.Interface().(*anypb.Any); ok {
		nestedMsg, err := anyMsg.UnmarshalNew()
		if err != nil {
			// Typed extensions whose Go types aren't linked into this binary will fail here;
			// that's expected, but log at debug so genuinely malformed configs are diagnosable.
			logger.Debug("skipping typed_config during proto reference scan",
				"scan", scanLabel, "type_url", anyMsg.GetTypeUrl(), "error", err)
			return
		}
		walkProtoMessages(nestedMsg, scanLabel, visit)
		return
	}

	walkProtoMessages(msg.Interface(), scanLabel, visit)
}
