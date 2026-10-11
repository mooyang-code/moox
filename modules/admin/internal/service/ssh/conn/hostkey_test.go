package conn

import (
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type memoryPins struct {
	mu   sync.Mutex
	pins map[string]string
}

// PinHostKey 与 DAO 的语义一致：已有记录不改，没有记录则记录，返回生效的指纹。
func (m *memoryPins) PinHostKey(id int, address, fingerprint string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pins[address] == "" {
		m.pins[address] = fingerprint
	}
	return m.pins[address], nil
}

func publicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return key
}

// 服务端主机密钥必须校验：首次记录，之后不一致拒绝；没有配置存储时一律拒绝。
func TestHostKeyCallbackPinsFirstKeyAndRejectsMismatch(t *testing.T) {
	pins := &memoryPins{pins: map[string]string{}}
	c := &SSHConn{Host: &model.SSHHost{ID: 1, Address: "10.0.0.1", Port: 22}, HostKeys: pins}
	first, other := publicKey(t), publicKey(t)
	require.NoError(t, c.hostKeyCallback()("", nil, first), "首次连接记录指纹")
	require.NoError(t, c.hostKeyCallback()("", nil, first), "同一密钥可继续连接")
	err := c.hostKeyCallback()("", nil, other)
	require.ErrorContains(t, err, "不一致")
	require.NoError(t, c.hostKeyCallback()("", nil, first), "不一致不得覆盖记录")

	unconfigured := &SSHConn{Host: &model.SSHHost{ID: 1, Address: "10.0.0.1", Port: 22}}
	require.ErrorContains(t, unconfigured.hostKeyCallback()("", nil, first), "未配置")
}

// 终端输入与清理线程并发读写活跃时间（用 -race 运行）。
func TestLastActiveTimeIsSafeUnderConcurrentRefreshAndCleanup(t *testing.T) {
	c := &SSHConn{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				c.RefreshActiveTime()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = c.LastActive()
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// 两个首次连接收到不同的主机密钥并发到达：只有一个会被信任，另一个必须在认证前失败。
func TestHostKeyCallbackConcurrentFirstConnectionsTrustOnlyOne(t *testing.T) {
	pins := &memoryPins{pins: map[string]string{}}
	c := &SSHConn{Host: &model.SSHHost{ID: 1, Address: "10.0.0.1", Port: 22}, HostKeys: pins}
	keys := []ssh.PublicKey{publicKey(t), publicKey(t)}
	results := make(chan error, len(keys))
	start := make(chan struct{})
	for _, key := range keys {
		go func(key ssh.PublicKey) {
			<-start
			results <- c.hostKeyCallback()("", nil, key)
		}(key)
	}
	close(start)
	failures := 0
	for range keys {
		if err := <-results; err != nil {
			failures++
		}
	}
	require.Equal(t, 1, failures, "恰好一个连接被信任")
}
