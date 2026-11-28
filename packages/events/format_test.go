package events

import (
	"testing"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestFormatDigestDeterministicAndSensitiveToSchemaChanges(t *testing.T) {
	base, err := FormatDigest()
	require.NoError(t, err)
	require.Contains(t, base, "sha256:")

	again, err := FormatDigest()
	require.NoError(t, err)
	require.Equal(t, base, again)

	// 修改某条事件的负载字段后，摘要必须变化。
	reg, err := DefaultRegistry()
	require.NoError(t, err)
	envelope := (&eventpb.EventMessage{}).ProtoReflect().Descriptor()

	buildSpecs := func(overrideName string, overrideDesc protoreflect.MessageDescriptor) []EventDescriptorSpec {
		events := reg.Events()
		out := make([]EventDescriptorSpec, 0, len(events))
		for _, ev := range events {
			desc := ev.NewPayload().ProtoReflect().Descriptor()
			if ev.Name() == overrideName && overrideDesc != nil {
				desc = overrideDesc
			}
			out = append(out, EventDescriptorSpec{
				Name:       ev.Name(),
				Version:    ev.Version(),
				Stream:     ev.Stream(),
				Descriptor: desc,
			})
		}
		return out
	}

	baselineFromSpecs, err := FormatDigestFromSpecs(Streams(), Consumers(), envelope, buildSpecs("", nil))
	require.NoError(t, err)
	require.Equal(t, base, baselineFromSpecs)

	// 1. 改事件负载字段：给 DatasetPeriodCompleted 追加一个字段。
	origDesc := DatasetPeriodCompleted.NewPayload().ProtoReflect().Descriptor()
	origFile := protodesc.ToFileDescriptorProto(origDesc.ParentFile())
	withExtraField := proto.Clone(origFile).(*descriptorpb.FileDescriptorProto)
	for _, msg := range withExtraField.MessageType {
		if msg.GetName() == string(origDesc.Name()) {
			msg.Field = append(msg.Field, &descriptorpb.FieldDescriptorProto{
				Name:     proto.String("extra_probe_field"),
				Number:   proto.Int32(99),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("extraProbeField"),
			})
		}
	}
	modFile, err := protodesc.NewFile(withExtraField, nil)
	require.NoError(t, err)
	modDesc := modFile.Messages().ByName(origDesc.Name())
	require.NotNil(t, modDesc)

	changedField, err := FormatDigestFromSpecs(Streams(), Consumers(), envelope, buildSpecs(DatasetPeriodCompleted.Name(), modDesc))
	require.NoError(t, err)
	require.NotEqual(t, base, changedField, "事件负载字段变化必须改变事件格式摘要")

	// 2. 改事件负载所在 proto 包名（模拟 3M-3 换包）。
	movedPkg := proto.Clone(origFile).(*descriptorpb.FileDescriptorProto)
	oldPrefix := "." + origFile.GetPackage() + "."
	newPrefix := ".moox.events.v1."
	movedPkg.Package = proto.String("moox.events.v1")
	for _, msg := range movedPkg.MessageType {
		for _, field := range msg.Field {
			if tn := field.GetTypeName(); len(tn) > len(oldPrefix) && tn[:len(oldPrefix)] == oldPrefix {
				field.TypeName = proto.String(newPrefix + tn[len(oldPrefix):])
			}
		}
	}
	movedFile, err := protodesc.NewFile(movedPkg, nil)
	require.NoError(t, err)
	movedDesc := movedFile.Messages().ByName(origDesc.Name())
	require.NotNil(t, movedDesc)

	changedPkg, err := FormatDigestFromSpecs(Streams(), Consumers(), envelope, buildSpecs(DatasetPeriodCompleted.Name(), movedDesc))
	require.NoError(t, err)
	require.NotEqual(t, base, changedPkg, "事件负载换包必须改变事件格式摘要")

	// 3. 改 Stream 配置。
	streams := Streams()
	streams[0].MaxBytes++
	changedStream, err := FormatDigestFromSpecs(streams, Consumers(), envelope, buildSpecs("", nil))
	require.NoError(t, err)
	require.NotEqual(t, base, changedStream, "Stream 声明变化必须改变事件格式摘要")

	// 4. 改消费者声明。
	consumers := Consumers()
	consumers[0].name += "_v2"
	changedConsumer, err := FormatDigestFromSpecs(Streams(), consumers, envelope, buildSpecs("", nil))
	require.NoError(t, err)
	require.NotEqual(t, base, changedConsumer, "消费者声明变化必须改变事件格式摘要")
}
