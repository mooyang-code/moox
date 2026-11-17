// Package session 是浏览器登录会话：只放在 Admin 进程内存里，Admin 重启后要重新登录。
//
// 会话 ID 只出现在 HttpOnly Cookie 里，不进任何响应体，也不写日志。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// CookieName 是会话 Cookie 的名字。__Host- 前缀让浏览器只接受 Secure、Path=/、不带 Domain 的这个 Cookie，
// 别的子域或明文页面写不进同名 Cookie；控制台下发时的属性正好满足。
const CookieName = "__Host-moox_session"

// Session 是一次登录。过期时间从登录时算起，不随使用顺延。
type Session struct {
	ID        string
	UserID    string
	Username  string
	ExpiresAt time.Time
}

// Store 保存全部会话。系统只有一个用户，会话很少；过期的在新建会话时顺手清掉，不另起定时器。
type Store struct {
	mu       sync.Mutex
	ttl      time.Duration
	now      func() time.Time
	sessions map[string]Session
}

// NewStore 创建会话表，ttl 是会话有效期。
func NewStore(ttl time.Duration) (*Store, error) {
	if ttl <= 0 {
		return nil, errors.New("会话有效期必须大于 0")
	}
	return &Store{ttl: ttl, now: time.Now, sessions: make(map[string]Session)}, nil
}

// TTL 是会话有效期，Cookie 的过期时间与它一致。
func (s *Store) TTL() time.Duration { return s.ttl }

// Create 为用户新建会话，会话 ID 是 32 字节随机数。
func (s *Store) Create(userID, username string) (Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, existing := range s.sessions {
		if !now.Before(existing.ExpiresAt) {
			delete(s.sessions, id)
		}
	}
	created := Session{ID: hex.EncodeToString(raw), UserID: userID, Username: username, ExpiresAt: now.Add(s.ttl)}
	s.sessions[created.ID] = created
	return created, nil
}

// Get 返回未过期的会话；过期的当场删除。
func (s *Store) Get(id string) (Session, bool) {
	if id == "" {
		return Session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found, ok := s.sessions[id]
	if !ok {
		return Session{}, false
	}
	if !s.now().Before(found.ExpiresAt) {
		delete(s.sessions, id)
		return Session{}, false
	}
	return found, true
}

// Delete 删除会话（退出登录）。
func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// Login 是控制台交给登录接口的请求来源与登录结果。来源 IP 和浏览器标识由 HTTP 层填写（请求体里的不可信）；
// 登录成功后登录接口把新会话写进 Issued，HTTP 层据此下发 Cookie。
type Login struct {
	ClientIP  string
	UserAgent string
	Issued    *Session
}

type loginKey struct{}

// WithLogin 把登录上下文放进 ctx，只有控制台的 HTTP 层调用。
func WithLogin(ctx context.Context, login *Login) context.Context {
	return context.WithValue(ctx, loginKey{}, login)
}

// LoginFrom 取出 WithLogin 放进的登录上下文；不经控制台入口时为 nil。
func LoginFrom(ctx context.Context) *Login {
	login, _ := ctx.Value(loginKey{}).(*Login)
	return login
}
