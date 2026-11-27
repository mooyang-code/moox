package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/events"
)

// EventBus 的账号、令牌在秘钥表里的 key_id、凭据文件名与权限都从 events.Roles 推出，这里不再另记一份。

// eventBusRoles 是全部账号的 NATS 用户名，顺序与 events.Roles 一致。
var eventBusRoles = func() []string {
	out := make([]string, 0, len(events.Roles))
	for _, role := range events.Roles {
		out = append(out, role.Username)
	}
	return out
}()

// eventBusKeys 是账号 → 令牌在秘钥表里的 key_id。
var eventBusKeys = func() map[string]string {
	out := make(map[string]string, len(events.Roles))
	for _, role := range events.Roles {
		out[role.Username] = role.SecretKey
	}
	return out
}()

// eventBusRoleFiles 是账号 → 凭据文件名。
func eventBusRoleFiles() map[string]string {
	out := make(map[string]string, len(events.Roles))
	for _, role := range events.Roles {
		out[role.Username] = role.File + ".yaml"
	}
	return out
}

// eventBusRoleACL 推出一个账号的发布、订阅权限。发布权限只到自己发布的事件主题族与自己消费者的 JetStream 接口；
// 拉取消费只需要订阅自己的收件箱，不订阅事件主题。
func eventBusRoleACL(role events.Role) (publish, subscribe []string) {
	if role.Admin {
		return []string{"$JS.API.>"}, []string{"_INBOX.>", "$JS.EVENT.ADVISORY.API"}
	}
	for _, event := range role.Publishes {
		publish = append(publish, fmt.Sprintf("moox.%s.v%d.>", event.Name(), event.Version()))
	}
	if len(role.Consumers) > 0 {
		publish = append(publish, "$JS.API.STREAM.NAMES")
	}
	for _, consumer := range role.Consumers {
		name := consumer.Name()
		stream := consumer.Stream()
		publish = append(publish,
			fmt.Sprintf("$JS.API.CONSUMER.INFO.*.%s", name),
			fmt.Sprintf("$JS.API.CONSUMER.CREATE.%s.%s", stream, name),
			fmt.Sprintf("$JS.API.CONSUMER.CREATE.%s.%s.>", stream, name),
			fmt.Sprintf("$JS.API.CONSUMER.DURABLE.CREATE.%s.%s", stream, name),
			fmt.Sprintf("$JS.API.CONSUMER.MSG.NEXT.%s.%s", stream, name),
			fmt.Sprintf("$JS.ACK.%s.%s.>", stream, name),
		)
		if consumer.Deletable() {
			publish = append(publish, fmt.Sprintf("$JS.API.CONSUMER.DELETE.%s.%s", stream, name))
		}
	}
	return publish, []string{"_INBOX.>"}
}

// usersYAML 生成 broker 的用户表。
func usersYAML(tokens map[string]string) string {
	var out strings.Builder
	out.WriteString("users:\n")
	for _, role := range events.Roles {
		publish, subscribe := eventBusRoleACL(role)
		fmt.Fprintf(&out, "  - username: %s\n    password: %s\n    permissions:\n      publish: {allow: [%s]}\n      subscribe: {allow: [%s]}\n",
			role.Username, tokens[role.Username], quoteSubjects(publish), quoteSubjects(subscribe))
	}
	return out.String()
}

func quoteSubjects(subjects []string) string {
	quoted := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		quoted = append(quoted, strconv.Quote(subject))
	}
	return strings.Join(quoted, ", ")
}
