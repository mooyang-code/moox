// Package config owns the explicit, validated EventBus configuration.
package config

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	if strings.TrimSpace(c.Broker.Host) == "" || c.Broker.Port < 1 || c.Broker.Port > 65535 {
		return fmt.Errorf("broker host and port are invalid")
	}
	if strings.TrimSpace(c.Broker.ServerName) == "" {
		return fmt.Errorf("broker.server_name must not be empty")
	}
	if c.Broker.Cluster.Enabled && c.Broker.ServerName == "eventbus-dev-1" {
		return fmt.Errorf("broker.server_name must be unique when cluster is enabled")
	}
	if c.Broker.Cluster.Enabled && (len(c.Broker.Cluster.Routes) > 0 || !isLoopback(c.Broker.Cluster.Host)) {
		return fmt.Errorf("non-loopback cluster routes are not supported in V1")
	}
	if c.Broker.MaxPayloadBytes <= 0 || c.Broker.MaxPayloadBytes > 64*1024*1024 {
		return fmt.Errorf("broker.max_payload_bytes must be between 1 and 67108864")
	}
	if unsafeStoreDir(c.Broker.StoreDir) {
		return fmt.Errorf("broker.store_dir %q is unsafe", c.Broker.StoreDir)
	}
	if c.Broker.Auth.Enabled {
		if strings.TrimSpace(c.Broker.Auth.UsersFile) == "" && (strings.TrimSpace(c.Broker.Auth.Username) == "" || c.Broker.Auth.Password == "") {
			return fmt.Errorf("broker.auth requires users_file or username/password")
		}
		if c.Broker.Auth.UsersFile != "" && (c.Broker.Auth.Username != "" || c.Broker.Auth.Password != "") {
			return fmt.Errorf("broker.auth users_file cannot be combined with single username/password")
		}
		if c.Broker.Auth.UsersFile != "" && strings.TrimSpace(c.InternalClient.CredentialFile) == "" {
			return fmt.Errorf("internal_client.credential_file is required when broker.auth.users_file is enabled")
		}
	}
	if c.Broker.TLS.Enabled && (strings.TrimSpace(c.Broker.TLS.CertFile) == "" || strings.TrimSpace(c.Broker.TLS.KeyFile) == "") {
		return fmt.Errorf("broker.tls requires cert_file and key_file")
	}
	if publicHost(c.Broker.Host) || publicHost(strings.TrimSpace(c.Broker.ClientAdvertise)) {
		if !c.Broker.Auth.Enabled || !c.Broker.TLS.Enabled || strings.TrimSpace(c.Broker.TLS.CAFile) == "" {
			return fmt.Errorf("non-loopback broker requires authentication and TLS CA")
		}
	}
	seenStreams := map[string]struct{}{}
	for i := range c.Streams {
		s := &c.Streams[i]
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("streams[%d].name is required", i)
		}
		if _, ok := seenStreams[s.Name]; ok {
			return fmt.Errorf("duplicate stream name %q", s.Name)
		}
		seenStreams[s.Name] = struct{}{}
		if s.Retention != "limits" && s.Retention != "work_queue" {
			return fmt.Errorf("stream %q retention %q is invalid", s.Name, s.Retention)
		}
		if s.Storage != "file" && s.Storage != "memory" {
			return fmt.Errorf("stream %q storage %q is invalid", s.Name, s.Storage)
		}
		if s.Replicas < 1 {
			return fmt.Errorf("stream %q replicas must be positive", s.Name)
		}
		if s.Replicas > 1 {
			return fmt.Errorf("stream %q replicas=%d are not supported in V1 standalone mode", s.Name, s.Replicas)
		}
		if len(s.Subjects) == 0 {
			return fmt.Errorf("stream %q must have subjects", s.Name)
		}
		for _, subject := range s.Subjects {
			if err := validateSubject(subject, true); err != nil {
				return fmt.Errorf("stream %q subject: %w", s.Name, err)
			}
		}
	}
	for i := range c.Streams {
		for j := i + 1; j < len(c.Streams); j++ {
			for _, left := range c.Streams[i].Subjects {
				for _, right := range c.Streams[j].Subjects {
					if patternsOverlap(left, right) {
						return fmt.Errorf("stream subjects %q and %q overlap", left, right)
					}
				}
			}
		}
	}
	seenTopics := map[string]struct{}{}
	for i := range c.Topics {
		t := &c.Topics[i]
		if !t.Enabled {
			continue
		}
		if err := validateSubject(t.Topic, false); err != nil {
			return fmt.Errorf("topics[%d]: %w", i, err)
		}
		if t.PayloadVersion == 0 {
			return fmt.Errorf("topic %q payload_version must be positive", t.Topic)
		}
		if !validPayloadContentType(t.PayloadContentType) {
			return fmt.Errorf("topic %q payload_content_type must be application/json or a protobuf message", t.Topic)
		}
		if version, err := topicVersion(t.Topic); err != nil || version != t.PayloadVersion {
			return fmt.Errorf("topic %q must end in .v<major> matching payload_version=%d", t.Topic, t.PayloadVersion)
		}
		if _, ok := seenTopics[t.Topic]; ok {
			return fmt.Errorf("duplicate topic %q", t.Topic)
		}
		seenTopics[t.Topic] = struct{}{}
		matches := 0
		matchedStream := ""
		for _, s := range c.Streams {
			for _, subject := range s.Subjects {
				if subjectMatches(subject, t.Topic) {
					matches++
					matchedStream = s.Name
					if t.Stream != "" && t.Stream != s.Name {
						return fmt.Errorf("topic %q stream %q does not cover subject", t.Topic, t.Stream)
					}
					break
				}
			}
		}
		if matches != 1 {
			return fmt.Errorf("topic %q must be covered by exactly one stream, got %d", t.Topic, matches)
		}
		if t.Stream == "" {
			t.Stream = matchedStream
		}
	}
	seenFamilies := map[string]struct{}{}
	for i := range c.TopicFamilies {
		f := &c.TopicFamilies[i]
		if !f.Enabled {
			continue
		}
		if err := validateSubject(f.Pattern, true); err != nil {
			return fmt.Errorf("topic_families[%d]: %w", i, err)
		}
		if strings.HasPrefix(f.Pattern, "moox.cloudnode.exec.v1.jobitem.") && !validCloudNodeFamily(f.Pattern) {
			return fmt.Errorf("topic family %q has invalid CloudNode route shape", f.Pattern)
		}
		if f.PayloadVersion == 0 {
			return fmt.Errorf("topic family %q payload_version must be positive", f.Pattern)
		}
		if !validPayloadContentType(f.PayloadContentType) {
			return fmt.Errorf("topic family %q payload_content_type must be application/json or a protobuf message", f.Pattern)
		}
		if _, ok := seenFamilies[f.Pattern]; ok {
			return fmt.Errorf("duplicate topic family %q", f.Pattern)
		}
		seenFamilies[f.Pattern] = struct{}{}
		matches := 0
		for _, s := range c.Streams {
			for _, subject := range s.Subjects {
				if patternsOverlap(subject, f.Pattern) {
					matches++
					if f.Stream != "" && f.Stream != s.Name {
						return fmt.Errorf("topic family %q stream %q does not cover subject", f.Pattern, f.Stream)
					}
				}
			}
		}
		if matches != 1 {
			return fmt.Errorf("topic family %q must be covered by exactly one stream, got %d", f.Pattern, matches)
		}
	}
	for i := range c.TopicFamilies {
		for j := i + 1; j < len(c.TopicFamilies); j++ {
			if c.TopicFamilies[i].Enabled && c.TopicFamilies[j].Enabled && patternsOverlap(c.TopicFamilies[i].Pattern, c.TopicFamilies[j].Pattern) {
				return fmt.Errorf("topic families %q and %q overlap", c.TopicFamilies[i].Pattern, c.TopicFamilies[j].Pattern)
			}
		}
	}
	for i := range c.Consumers {
		consumer := &c.Consumers[i]
		if err := validateConsumer(consumer, c); err != nil {
			return err
		}
	}
	for i := range c.ConsumerTemplates {
		template := &c.ConsumerTemplates[i]
		if err := validateConsumerTemplate(template, c); err != nil {
			return err
		}
	}
	seenKV := map[string]struct{}{}
	for i := range c.KV {
		k := &c.KV[i]
		if strings.TrimSpace(k.Bucket) == "" {
			return fmt.Errorf("kv[%d].bucket is required", i)
		}
		if _, ok := seenKV[k.Bucket]; ok {
			return fmt.Errorf("duplicate kv bucket %q", k.Bucket)
		}
		seenKV[k.Bucket] = struct{}{}
		if k.Replicas < 1 || k.History < 1 {
			return fmt.Errorf("kv %q replicas/history must be positive", k.Bucket)
		}
		if k.Replicas > 1 {
			return fmt.Errorf("kv %q replicas=%d are not supported in V1 standalone mode", k.Bucket, k.Replicas)
		}
	}
	return nil
}

func isLoopback(host string) bool {
	host = strings.TrimSpace(host)
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}

func publicHost(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.Contains(value, ":") && !strings.Contains(value, "]") {
		if host, _, err := net.SplitHostPort(value); err == nil {
			value = host
		}
	}
	return value == "0.0.0.0" || value == "::" || !isLoopback(value)
}

func validateConsumer(c *ConsumerConfig, cfg *Config) error {
	if strings.TrimSpace(c.Stream) == "" || strings.TrimSpace(c.Durable) == "" || strings.TrimSpace(c.FilterSubject) == "" {
		return fmt.Errorf("consumer stream, durable, and filter_subject are required")
	}
	if c.AckPolicy == "" {
		c.AckPolicy = "explicit"
	}
	if c.DeliverPolicy == "" {
		c.DeliverPolicy = "all"
	}
	if c.ReplayPolicy == "" {
		c.ReplayPolicy = "instant"
	}
	if c.AckPolicy != "explicit" || (c.DeliverPolicy != "all" && c.DeliverPolicy != "new") || c.ReplayPolicy != "instant" {
		return fmt.Errorf("consumer %q has unsupported policy", c.Durable)
	}
	if c.AckWait <= 0 || c.MaxAckPending <= 0 || c.MaxDeliver == 0 {
		return fmt.Errorf("consumer %q has invalid ack/max settings", c.Durable)
	}
	if _, ok := findStream(cfg, c.Stream); !ok {
		return fmt.Errorf("consumer %q references unknown stream %q", c.Durable, c.Stream)
	}
	if err := validateSubject(c.FilterSubject, true); err != nil {
		return fmt.Errorf("consumer %q filter: %w", c.Durable, err)
	}
	covered := false
	for _, t := range cfg.Topics {
		if t.Enabled && t.Topic == c.FilterSubject && t.Stream == c.Stream {
			covered = true
		}
	}
	if !covered {
		for _, f := range cfg.TopicFamilies {
			if f.Enabled && f.Stream == c.Stream && patternCovers(c.FilterSubject, f.Pattern) {
				covered = true
			}
		}
	}
	if !covered {
		return fmt.Errorf("consumer %q filter %q is not registered", c.Durable, c.FilterSubject)
	}
	return nil
}

func patternCovers(cover, subjectPattern string) bool {
	coverParts := strings.Split(cover, ".")
	subjectParts := strings.Split(subjectPattern, ".")
	for i, part := range coverParts {
		if part == ">" {
			return i < len(subjectParts)
		}
		if i >= len(subjectParts) || (part != "*" && part != subjectParts[i]) {
			return false
		}
	}
	return len(coverParts) == len(subjectParts)
}

func validPayloadContentType(value string) bool {
	value = strings.TrimSpace(value)
	if value == "application/json" {
		return true
	}
	return strings.HasPrefix(value, "application/x-protobuf; message=") && len(strings.TrimPrefix(value, "application/x-protobuf; message=")) > 0
}

func validCloudNodeFamily(pattern string) bool {
	parts := strings.Split(pattern, ".")
	if len(parts) != 11 || parts[0] != "moox" || parts[1] != "cloudnode" || parts[2] != "exec" || parts[3] != "v1" || parts[4] != "jobitem" || parts[5] != "s" || parts[7] != "pkg" || parts[9] != "type" {
		return false
	}
	return parts[6] == "*" && parts[8] == "*" && parts[10] == "*"
}

func validateConsumerTemplate(c *ConsumerTemplateConfig, cfg *Config) error {
	if strings.TrimSpace(c.Stream) == "" || strings.TrimSpace(c.DurablePrefix) == "" || strings.TrimSpace(c.FilterPattern) == "" {
		return fmt.Errorf("consumer template stream, durable_prefix, and filter_pattern are required")
	}
	if _, ok := findStream(cfg, c.Stream); !ok {
		return fmt.Errorf("consumer template references unknown stream %q", c.Stream)
	}
	if err := validateSubject(c.FilterPattern, true); err != nil {
		return fmt.Errorf("consumer template filter: %w", err)
	}
	if c.AckPolicy == "" {
		c.AckPolicy = "explicit"
	}
	if c.DeliverPolicy == "" {
		c.DeliverPolicy = "all"
	}
	if c.ReplayPolicy == "" {
		c.ReplayPolicy = "instant"
	}
	if c.AckPolicy != "explicit" || c.DeliverPolicy != "all" || c.ReplayPolicy != "instant" || c.AckWait <= 0 || c.MaxAckPending <= 0 || c.MaxDeliver == 0 {
		return fmt.Errorf("consumer template %q has invalid policy or limits", c.DurablePrefix)
	}
	return nil
}

func findStream(c *Config, name string) (StreamConfig, bool) {
	for _, stream := range c.Streams {
		if stream.Name == name {
			return stream, true
		}
	}
	return StreamConfig{}, false
}

func unsafeStoreDir(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return true
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	return abs == string(filepath.Separator) || abs == filepath.Dir(abs) && abs == "/"
}

func validateSubject(subject string, wildcard bool) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return fmt.Errorf("subject is empty")
	}
	tokens := strings.Split(subject, ".")
	for i, token := range tokens {
		if token == "" || strings.ContainsAny(token, " \t\r\n") {
			return fmt.Errorf("subject %q contains invalid token", subject)
		}
		if token == ">" && (!wildcard || i != len(tokens)-1) {
			return fmt.Errorf("subject %q has invalid > wildcard", subject)
		}
		if token == "*" && !wildcard {
			return fmt.Errorf("subject %q must be concrete", subject)
		}
	}
	return nil
}

func topicVersion(topic string) (uint32, error) {
	idx := strings.LastIndex(topic, ".v")
	if idx < 0 || idx+2 >= len(topic) {
		return 0, fmt.Errorf("missing version suffix")
	}
	value, err := strconv.ParseUint(topic[idx+2:], 10, 32)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("invalid version suffix")
	}
	return uint32(value), nil
}

func subjectMatches(pattern, subject string) bool {
	p, s := strings.Split(pattern, "."), strings.Split(subject, ".")
	for i := 0; i < len(p); i++ {
		if p[i] == ">" {
			return i < len(s)
		}
		if i >= len(s) || (p[i] != "*" && p[i] != s[i]) {
			return false
		}
	}
	return len(p) == len(s)
}

func patternsOverlap(a, b string) bool {
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; ; i++ {
		if i >= len(aa) || i >= len(bb) {
			return i >= len(aa) && i >= len(bb)
		}
		if aa[i] == ">" || bb[i] == ">" {
			return true
		}
		if aa[i] != "*" && bb[i] != "*" && aa[i] != bb[i] {
			return false
		}
	}
}
