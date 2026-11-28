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
	baseMsg, err := MessagesFormatDigest()
	require.NoError(t, err)
	require.Contains(t, baseMsg, "sha256:")

	againMsg, err := MessagesFormatDigest()
	require.NoError(t, err)
	require.Equal(t, baseMsg, againMsg)

	baseTopo := TopologyFormatDigest()
	require.Contains(t, baseTopo, "sha256:")
	require.Equal(t, baseTopo, TopologyFormatDigest())

	// 修改某条事件的负载字段后，事件消息摘要必须变化。
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

	baselineFromSpecs, err := MessagesFormatDigestFromSpecs(envelope, buildSpecs("", nil))
	require.NoError(t, err)
	require.Equal(t, baseMsg, baselineFromSpecs)
	require.Equal(t, baseTopo, TopologyFormatDigestFromSpecs(Streams(), Consumers()))

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

	changedField, err := MessagesFormatDigestFromSpecs(envelope, buildSpecs(DatasetPeriodCompleted.Name(), modDesc))
	require.NoError(t, err)
	require.NotEqual(t, baseMsg, changedField, "事件负载字段变化必须改变事件消息摘要")

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

	changedPkg, err := MessagesFormatDigestFromSpecs(envelope, buildSpecs(DatasetPeriodCompleted.Name(), movedDesc))
	require.NoError(t, err)
	require.NotEqual(t, baseMsg, changedPkg, "事件负载换包必须改变事件消息摘要")

	// 3. 只改 Stream 的容量、保留期、描述或消费者的可删除权限等运行参数，拓扑摘要必须不变。
	tunedStreams := Streams()
	tunedStreams[0].MaxAge *= 2
	tunedStreams[0].MaxBytes *= 4
	tunedStreams[0].Description = "adjusted runtime description"
	tunedConsumers := Consumers()
	tunedConsumers[0].deletable = !tunedConsumers[0].deletable
	require.Equal(t, baseTopo, TopologyFormatDigestFromSpecs(tunedStreams, tunedConsumers),
		"只调整 Stream 容量/保留期/描述或消费者权限参数不应改变拓扑摘要")

	// 4. 改 Stream 名称、主题或保留策略（WorkQueue）必须改变 EventBus 拓扑摘要。
	renamedStreams := Streams()
	renamedStreams[0].Name += "_V2"
	require.NotEqual(t, baseTopo, TopologyFormatDigestFromSpecs(renamedStreams, Consumers()), "Stream 名称变化必须改变拓扑摘要")

	changedSubjects := Streams()
	changedSubjects[0].Subjects = append(changedSubjects[0].Subjects, "moox.probe.v1.>")
	require.NotEqual(t, baseTopo, TopologyFormatDigestFromSpecs(changedSubjects, Consumers()), "Stream 主题变化必须改变拓扑摘要")

	changedRetention := Streams()
	changedRetention[0].WorkQueue = !changedRetention[0].WorkQueue
	require.NotEqual(t, baseTopo, TopologyFormatDigestFromSpecs(changedRetention, Consumers()), "Stream 保留策略变化必须改变拓扑摘要")

	// 5. 改消费者名称或过滤主题必须改变 EventBus 拓扑摘要。
	consumers := Consumers()
	consumers[0].name += "_v2"
	require.NotEqual(t, baseTopo, TopologyFormatDigestFromSpecs(Streams(), consumers), "消费者名称变化必须改变拓扑摘要")

	changedFilterConsumers := Consumers()
	changedFilterConsumers[0].events = append(changedFilterConsumers[0].events, InstrumentDelistScheduled)
	require.NotEqual(t, baseTopo, TopologyFormatDigestFromSpecs(Streams(), changedFilterConsumers), "消费者过滤主题变化必须改变拓扑摘要")
}
