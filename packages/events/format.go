package events

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"slices"
	"sort"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// EventDescriptorSpec 描述一条事件及其负载的 proto 消息描述符，供格式摘要计算与测试使用。
type EventDescriptorSpec struct {
	Name       string
	Version    uint32
	Stream     string
	Descriptor protoreflect.MessageDescriptor
}

// Consumers 返回拓扑声明里的全部持久消费者（去重并按名字排序）。
func Consumers() []DurableConsumer {
	seen := make(map[string]DurableConsumer)
	for _, role := range Roles {
		for _, c := range role.Consumers {
			seen[c.Name()] = c
		}
	}
	out := make([]DurableConsumer, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// MessagesFormatDigest 计算磁盘上持久化事件消息的格式摘要：
// 覆盖事件信封描述符以及全部已注册事件的名称、版本与负载消息描述符。
// storage-primary 与 EventBus 都依赖它；Stream 与消费者配置只进 TopologyFormatDigest，
// 调整 Stream 容量上限等拓扑参数不会让 storage-primary 被判格式变化。
func MessagesFormatDigest() (string, error) {
	reg, err := DefaultRegistry()
	if err != nil {
		return "", err
	}
	return MessagesFormatDigestFor(reg)
}

// MessagesFormatDigestFor 按给定注册表计算事件消息格式摘要（不含 Stream 与消费者声明）。
func MessagesFormatDigestFor(reg *Registry) (string, error) {
	if reg == nil {
		return "", fmt.Errorf("事件注册表不能为空")
	}
	registered := reg.Events()
	specs := make([]EventDescriptorSpec, 0, len(registered))
	for _, ev := range registered {
		payload := ev.NewPayload()
		if payload == nil {
			return "", fmt.Errorf("事件 %s 缺少负载定义", eventKey(ev))
		}
		specs = append(specs, EventDescriptorSpec{
			Name:       ev.Name(),
			Version:    ev.Version(),
			Stream:     ev.Stream(),
			Descriptor: payload.ProtoReflect().Descriptor(),
		})
	}
	envelope := (&eventpb.EventMessage{}).ProtoReflect().Descriptor()
	return MessagesFormatDigestFromSpecs(envelope, specs)
}

// MessagesFormatDigestFromSpecs 由给定的信封描述符与事件描述符列表计算事件消息的确定性 SHA-256 摘要。
// 不包含 Stream 名称、容量上限或消费者声明，仅覆盖事件信封及各事件的名称、版本与负载消息描述。
func MessagesFormatDigestFromSpecs(
	envelope protoreflect.MessageDescriptor,
	specs []EventDescriptorSpec,
) (string, error) {
	if envelope == nil {
		return "", fmt.Errorf("事件信封消息描述符不能为空")
	}
	h := sha256.New()

	// 1. 事件信封消息描述。
	fmt.Fprint(h, "envelope\x00")
	writeMessageDescriptor(h, envelope, map[protoreflect.FullName]bool{})

	// 2. 全部已注册事件的名称、版本与负载消息描述符（按名称、版本排序）。
	sortedSpecs := append([]EventDescriptorSpec(nil), specs...)
	sort.Slice(sortedSpecs, func(i, j int) bool {
		if sortedSpecs[i].Name != sortedSpecs[j].Name {
			return sortedSpecs[i].Name < sortedSpecs[j].Name
		}
		return sortedSpecs[i].Version < sortedSpecs[j].Version
	})
	fmt.Fprintf(h, "events\x00%d\x00", len(sortedSpecs))
	for _, spec := range sortedSpecs {
		if spec.Descriptor == nil {
			return "", fmt.Errorf("事件 %s@%d 缺少负载消息描述符", spec.Name, spec.Version)
		}
		fmt.Fprintf(h, "event\x00%s\x00%d\x00", spec.Name, spec.Version)
		writeMessageDescriptor(h, spec.Descriptor, map[protoreflect.FullName]bool{})
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// TopologyFormatDigest 计算 EventBus 的 Stream 与持久消费者声明摘要（仅用于 EventBus 数据格式）。
func TopologyFormatDigest() string {
	return TopologyFormatDigestFromSpecs(Streams(), Consumers())
}

// TopologyFormatDigestFromSpecs 由给定的 Stream 与持久消费者声明计算确定性 SHA-256 摘要。
func TopologyFormatDigestFromSpecs(streams []StreamSpec, consumers []DurableConsumer) string {
	h := sha256.New()

	// 1. Stream 声明（按名称排序）。
	sortedStreams := append([]StreamSpec(nil), streams...)
	sort.Slice(sortedStreams, func(i, j int) bool { return sortedStreams[i].Name < sortedStreams[j].Name })
	fmt.Fprintf(h, "streams\x00%d\x00", len(sortedStreams))
	for _, s := range sortedStreams {
		subjects := append([]string(nil), s.Subjects...)
		slices.Sort(subjects)
		fmt.Fprintf(h, "stream\x00%s\x00%t\x00%d\x00%d\x00%d\x00", s.Name, s.WorkQueue, s.MaxAge, s.MaxBytes, StreamDuplicates)
		for _, subj := range subjects {
			fmt.Fprintf(h, "subject\x00%s\x00", subj)
		}
	}

	// 2. 持久消费者声明（按名称排序）。
	sortedConsumers := append([]DurableConsumer(nil), consumers...)
	sort.Slice(sortedConsumers, func(i, j int) bool { return sortedConsumers[i].Name() < sortedConsumers[j].Name() })
	fmt.Fprintf(h, "consumers\x00%d\x00", len(sortedConsumers))
	for _, c := range sortedConsumers {
		filters := c.FilterSubjects()
		slices.Sort(filters)
		fmt.Fprintf(h, "consumer\x00%s\x00%s\x00%t\x00", c.Name(), c.Stream(), c.Deletable())
		for _, f := range filters {
			fmt.Fprintf(h, "filter\x00%s\x00", f)
		}
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// writeMessageDescriptor 递归写出消息的确定性结构摘要：包名+消息名、字段编号/名/类型/基数/oneof、引用的枚举与子消息。
func writeMessageDescriptor(h hash.Hash, md protoreflect.MessageDescriptor, visited map[protoreflect.FullName]bool) {
	fullName := md.FullName()
	fmt.Fprintf(h, "msg\x00%s\x00", fullName)
	if visited[fullName] {
		fmt.Fprint(h, "ref\x00")
		return
	}
	visited[fullName] = true

	fields := md.Fields()
	sortedFields := make([]protoreflect.FieldDescriptor, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		sortedFields = append(sortedFields, fields.Get(i))
	}
	sort.Slice(sortedFields, func(i, j int) bool { return sortedFields[i].Number() < sortedFields[j].Number() })

	fmt.Fprintf(h, "fields\x00%d\x00", len(sortedFields))
	for _, f := range sortedFields {
		oneofName := ""
		if oo := f.ContainingOneof(); oo != nil && !oo.IsSynthetic() {
			oneofName = string(oo.Name())
		}
		fmt.Fprintf(h, "field\x00%d\x00%s\x00%s\x00%d\x00%d\x00%t\x00%t\x00%t\x00%s\x00",
			f.Number(), f.Name(), f.JSONName(), f.Cardinality(), f.Kind(), f.IsMap(), f.HasPresence(), f.IsPacked(), oneofName)
		switch f.Kind() {
		case protoreflect.EnumKind:
			writeEnumDescriptor(h, f.Enum())
		case protoreflect.MessageKind, protoreflect.GroupKind:
			writeMessageDescriptor(h, f.Message(), visited)
		}
	}

	oneofs := md.Oneofs()
	oneofNames := make([]string, 0, oneofs.Len())
	for i := 0; i < oneofs.Len(); i++ {
		oo := oneofs.Get(i)
		if !oo.IsSynthetic() {
			oneofNames = append(oneofNames, string(oo.Name()))
		}
	}
	slices.Sort(oneofNames)
	fmt.Fprintf(h, "oneofs\x00%d\x00", len(oneofNames))
	for _, name := range oneofNames {
		fmt.Fprintf(h, "oneof\x00%s\x00", name)
	}
}

func writeEnumDescriptor(h hash.Hash, ed protoreflect.EnumDescriptor) {
	fmt.Fprintf(h, "enum\x00%s\x00", ed.FullName())
	values := ed.Values()
	sortedValues := make([]protoreflect.EnumValueDescriptor, 0, values.Len())
	for i := 0; i < values.Len(); i++ {
		sortedValues = append(sortedValues, values.Get(i))
	}
	sort.Slice(sortedValues, func(i, j int) bool {
		if sortedValues[i].Number() != sortedValues[j].Number() {
			return sortedValues[i].Number() < sortedValues[j].Number()
		}
		return sortedValues[i].Name() < sortedValues[j].Name()
	})
	for _, v := range sortedValues {
		fmt.Fprintf(h, "ev\x00%d\x00%s\x00", v.Number(), v.Name())
	}
}
