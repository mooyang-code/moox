// Package messagepublisher exposes the committed-row publisher contract at
// the DataShard ownership boundary. The transport implementation is shared
// with the ViewBuilder consumer adapter so bootstrap can use one connection.
package messagepublisher

import "github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder/eventconsumer"

type Publisher = eventconsumer.Publisher
type MessagePublisher = eventconsumer.Publisher
type ProducerBus = eventconsumer.ProducerBus
type Bus = eventconsumer.Bus
type MemoryBus = eventconsumer.MemoryBus

var NewProducerBus = eventconsumer.NewProducerBus
var NewMemoryBus = eventconsumer.NewMemoryBus
