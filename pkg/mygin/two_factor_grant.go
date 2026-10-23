package mygin

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/railzen/nezha-zero/model"
	"github.com/railzen/nezha-zero/service/singleton"
)

// 敏感操作二次验证授权：会话通过 TOTP 校验后，在 TwoFactorGrantTTL 内可执行
// 能直接在服务器上执行代码的操作（终端、文件管理、计划任务、NAT）。
// 授权状态只存内存，以会话 Token 摘要为键，面板重启或登出即失效；未启用双重验证时不做限制。
const (
	TwoFactorGrantTTL = 30 * time.Minute
	// TwoFactorGrantRequiredCode 需要二次验证时接口返回的业务码
	TwoFactorGrantRequiredCode = http.StatusPreconditionRequired
	// twoFactorGrantMaxFailures 同一会话在 TwoFactorGrantTTL 内允许的验证码试错次数，超出后需重新登录
	twoFactorGrantMaxFailures = 5
)

type twoFactorGrantState struct {
	grantedUntil time.Time
	failures     int
	failResetAt  time.Time
}

var (
	twoFactorGrantMu     sync.Mutex
	twoFactorGrantStates = make(map[string]*twoFactorGrantState)
)

// twoFactorGrantKey 仅超管的登录会话可获得授权；API Token 身份没有会话摘要。
func twoFactorGrantKey(c *gin.Context) (string, bool) {
	u, ok := c.Get(model.CtxKeyAuthorizedUser)
	if !ok {
		return "", false
	}
	user, ok := u.(*model.User)
	if !ok || !user.SuperAdmin || user.Token == "" {
		return "", false
	}
	return user.Token, true
}

// pruneTwoFactorGrantsLocked 清理授权与失败计数均已过期的条目，调用方需持有 twoFactorGrantMu。
func pruneTwoFactorGrantsLocked(now time.Time) {
	for k, s := range twoFactorGrantStates {
		if now.After(s.grantedUntil) && now.After(s.failResetAt) {
			delete(twoFactorGrantStates, k)
		}
	}
}

// TwoFactorGrantRequired 当前是否需要敏感操作二次验证（以是否启用双重验证为准）。
func TwoFactorGrantRequired() bool {
	return singleton.Conf.TwoFactorActive()
}

// TwoFactorGrantRemaining 当前会话的授权剩余时长，未授权返回 0。
func TwoFactorGrantRemaining(c *gin.Context) time.Duration {
	key, ok := twoFactorGrantKey(c)
	if !ok {
		return 0
	}
	twoFactorGrantMu.Lock()
	defer twoFactorGrantMu.Unlock()
	s, ok := twoFactorGrantStates[key]
	if !ok {
		return 0
	}
	if remaining := time.Until(s.grantedUntil); remaining > 0 {
		return remaining
	}
	return 0
}

// TwoFactorGranted 未启用双重验证，或当前会话处于授权有效期内。
func TwoFactorGranted(c *gin.Context) bool {
	return !TwoFactorGrantRequired() || TwoFactorGrantRemaining(c) > 0
}

// TwoFactorGrantLocked 当前会话试错次数是否已达上限。
func TwoFactorGrantLocked(c *gin.Context) bool {
	key, ok := twoFactorGrantKey(c)
	if !ok {
		return true
	}
	twoFactorGrantMu.Lock()
	defer twoFactorGrantMu.Unlock()
	s, ok := twoFactorGrantStates[key]
	return ok && s.failures >= twoFactorGrantMaxFailures && time.Now().Before(s.failResetAt)
}

// GrantTwoFactor 为当前会话授予 TwoFactorGrantTTL 的敏感操作授权，并清零试错计数。
func GrantTwoFactor(c *gin.Context) bool {
	key, ok := twoFactorGrantKey(c)
	if !ok {
		return false
	}
	now := time.Now()
	twoFactorGrantMu.Lock()
	defer twoFactorGrantMu.Unlock()
	pruneTwoFactorGrantsLocked(now)
	twoFactorGrantStates[key] = &twoFactorGrantState{grantedUntil: now.Add(TwoFactorGrantTTL)}
	return true
}

// RecordTwoFactorGrantFailure 记录一次验证码错误，返回剩余可试次数。
func RecordTwoFactorGrantFailure(c *gin.Context) int {
	key, ok := twoFactorGrantKey(c)
	if !ok {
		return 0
	}
	now := time.Now()
	twoFactorGrantMu.Lock()
	defer twoFactorGrantMu.Unlock()
	pruneTwoFactorGrantsLocked(now)
	s, ok := twoFactorGrantStates[key]
	if !ok {
		s = &twoFactorGrantState{}
		twoFactorGrantStates[key] = s
	}
	if now.After(s.failResetAt) {
		s.failures = 0
		s.failResetAt = now.Add(TwoFactorGrantTTL)
	}
	s.failures++
	return twoFactorGrantMaxFailures - s.failures
}

// RevokeTwoFactorGrant 撤销指定会话（Token 摘要）的授权，用于登出。
func RevokeTwoFactorGrant(sessionTokenHash string) {
	twoFactorGrantMu.Lock()
	defer twoFactorGrantMu.Unlock()
	delete(twoFactorGrantStates, sessionTokenHash)
}

// ResetTwoFactorGrants 清空全部授权，用于双重验证密钥变更。
func ResetTwoFactorGrants() {
	twoFactorGrantMu.Lock()
	defer twoFactorGrantMu.Unlock()
	twoFactorGrantStates = make(map[string]*twoFactorGrantState)
}

// RequireTwoFactorGrant 中间件：接口类敏感操作要求当前会话处于授权有效期内。
func RequireTwoFactorGrant() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !TwoFactorGranted(c) {
			c.JSON(http.StatusOK, model.Response{
				Code:    TwoFactorGrantRequiredCode,
				Message: "敏感操作授权已失效，请重新输入动态验证码",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
