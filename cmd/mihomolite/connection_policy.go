package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ConnectionPolicy is opt-in, independent of the Windows auto-start setting.
// A zero numeric field is a legacy/missing value; explicit out-of-range values
// are rejected rather than silently widening the retry budget.
type ConnectionPolicy struct {
	StartupConnect      bool `json:"startupConnect"`
	CrashReconnect      bool `json:"crashReconnect"`
	MaxRetries          int  `json:"maxRetries"`
	InitialDelaySeconds int  `json:"initialDelaySeconds"`
	MaxDelaySeconds     int  `json:"maxDelaySeconds"`
}

var errConnectionPolicy = errors.New("自动连接策略无效")
var errConnectionPolicyPermanent = errors.New("自动连接需要人工处理")
var errConnectionCancelled = errors.New("连接已取消")

const connectionStablePeriod = 60 * time.Second
const connectionBusyPoll = 100 * time.Millisecond

func normalizeConnectionPolicy(p ConnectionPolicy) (ConnectionPolicy, error) {
	if p.MaxRetries == 0 {
		p.MaxRetries = 3
	}
	if p.InitialDelaySeconds == 0 {
		p.InitialDelaySeconds = 2
	}
	if p.MaxDelaySeconds == 0 {
		p.MaxDelaySeconds = 30
	}
	if p.MaxRetries < 1 || p.MaxRetries > 5 {
		return p, fmt.Errorf("%w：重试次数须为 1–5", errConnectionPolicy)
	}
	if p.InitialDelaySeconds < 1 || p.InitialDelaySeconds > 60 {
		return p, fmt.Errorf("%w：初始延迟须为 1–60 秒", errConnectionPolicy)
	}
	if p.MaxDelaySeconds < p.InitialDelaySeconds || p.MaxDelaySeconds > 300 {
		return p, fmt.Errorf("%w：最大延迟须不小于初始延迟且不超过 300 秒", errConnectionPolicy)
	}
	return p, nil
}

func connectionRetryDelay(p ConnectionPolicy, attempt int) time.Duration {
	delay := time.Duration(p.InitialDelaySeconds) * time.Second
	maximum := time.Duration(p.MaxDelaySeconds) * time.Second
	for i := 1; i < attempt && delay < maximum; i++ {
		delay = min(delay*2, maximum)
	}
	return min(delay, maximum)
}

// Attempt counts actual crash reconnect starts, not busy wakeups or startup.
// A ready response does NOT reset it: a core must stay ready for 60 seconds.
type ConnectionRetryState struct {
	Waiting     bool       `json:"waiting"`
	Active      bool       `json:"active"`
	Attempt     int        `json:"attempt"`
	NextRetryAt *time.Time `json:"nextRetryAt,omitempty"`
	Exhausted   bool       `json:"exhausted"`
	Error       string     `json:"error"`
	Reason      string     `json:"reason,omitempty"`
	MaxRetries  int        `json:"maxRetries"`
}

// All fields except immutable worker arguments are guarded by Application.mu.
// Test seams are per application; they never replace a real proxy globally.
type connectionPolicyRuntime struct {
	ctx                 context.Context
	cancel              context.CancelFunc
	done                chan struct{}
	wake                chan struct{}
	generation          uint64
	operationGeneration uint64
	disconnectPending   bool
	startupDone         bool
	wanted              bool
	waiting             bool
	attempting          bool
	attempt             int
	nextRetryAt         time.Time
	exhausted           bool
	error               string
	reason              string
	readySession        *coreSession
	readyAt             time.Time
	connectContext      context.Context
	connectCancel       context.CancelFunc
	now                 func() time.Time
	start               func() error
}

func (a *Application) connectionNowLocked() time.Time {
	if a.connectionPolicy.now != nil {
		return a.connectionPolicy.now()
	}
	return time.Now()
}

func (a *Application) configuredConnectionPolicyLocked() ConnectionPolicy {
	if a.config == nil {
		p, _ := normalizeConnectionPolicy(ConnectionPolicy{})
		return p
	}
	p, err := normalizeConnectionPolicy(a.config.ConnectionPolicy)
	if err != nil {
		return ConnectionPolicy{}
	}
	return p
}

func (a *Application) connectionRetryStateLocked() ConnectionRetryState {
	r := &a.connectionPolicy
	s := ConnectionRetryState{Waiting: r.waiting, Active: r.waiting || r.attempting, Attempt: r.attempt, Exhausted: r.exhausted, Error: r.error, Reason: r.reason, MaxRetries: a.configuredConnectionPolicyLocked().MaxRetries}
	if r.waiting {
		at := r.nextRetryAt
		s.NextRetryAt = &at
	}
	return s
}

func (a *Application) wakeConnectionPolicyLocked() {
	if a.connectionPolicy.wake != nil {
		select {
		case a.connectionPolicy.wake <- struct{}{}:
		default:
		}
	}
}

func (a *Application) publishConnectionPolicyLocked() {
	if a.events != nil {
		a.broadcastLocked()
	}
	a.wakeConnectionPolicyLocked()
}

func (a *Application) cancelConnectionPolicyLocked() {
	r := &a.connectionPolicy
	r.generation++
	r.disconnectPending = false
	r.wanted, r.waiting = false, false
	r.nextRetryAt = time.Time{}
	r.attempt, r.exhausted, r.error, r.reason = 0, false, "", ""
	r.readySession, r.readyAt = nil, time.Time{}
	if r.connectCancel != nil {
		r.connectCancel()
	}
	a.publishConnectionPolicyLocked()
}

// Called before acquiring the operation slot: cancelling a waiting/starting
// automatic connection must work even while another operation is busy.
func (a *Application) cancelConnectionPolicy() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancelConnectionPolicyLocked()
}

// Call only after persisting the settings, with a.mu held. Enabling startup
// after launch never connects immediately; enabling crash reconnect never
// revives a manually disconnected session.
func (a *Application) connectionPolicyChangedLocked() {
	p := a.configuredConnectionPolicyLocked()
	r := &a.connectionPolicy
	if (r.reason == "startup" && !p.StartupConnect) || (r.reason == "crash" && !p.CrashReconnect) {
		a.cancelConnectionPolicyLocked()
		return
	}
	if r.waiting && r.reason == "crash" && r.attempt >= p.MaxRetries {
		a.exhaustConnectionPolicyLocked("已达到自动重连次数上限，请手动连接")
	}
	a.publishConnectionPolicyLocked()
}

func (a *Application) exhaustConnectionPolicyLocked(message string) {
	r := &a.connectionPolicy
	r.waiting, r.exhausted, r.error = false, true, message
	r.nextRetryAt = time.Time{}
	a.publishConnectionPolicyLocked()
}

func (a *Application) scheduleConnectionPolicyLocked(reason string) {
	r := &a.connectionPolicy
	p := a.configuredConnectionPolicyLocked()
	if !r.wanted || r.exhausted || a.shuttingDown || r.ctx == nil || r.ctx.Err() != nil || r.waiting {
		return
	}
	if reason == "crash" && !p.CrashReconnect {
		return
	}
	if reason == "crash" && r.attempt >= p.MaxRetries {
		a.exhaustConnectionPolicyLocked("已达到自动重连次数上限，请手动连接")
		return
	}
	r.reason, r.waiting = reason, true
	r.nextRetryAt = a.connectionNowLocked()
	if reason == "crash" {
		r.nextRetryAt = r.nextRetryAt.Add(connectionRetryDelay(p, r.attempt+1))
	}
	a.publishConnectionPolicyLocked()
}

// startConnectionPolicy is idempotent for the application lifetime. The
// startup connection is tried ONCE, even when readiness or validation fails.
func (a *Application) startConnectionPolicy(ctx context.Context) {
	a.mu.Lock()
	r := &a.connectionPolicy
	if r.done != nil {
		a.mu.Unlock()
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.done, r.wake = make(chan struct{}), make(chan struct{}, 1)
	workerCtx, done, wake := r.ctx, r.done, r.wake
	if !r.startupDone {
		r.startupDone = true
		if a.configuredConnectionPolicyLocked().StartupConnect && a.core == nil && !a.shuttingDown && r.generation == 0 {
			r.wanted = true
			a.scheduleConnectionPolicyLocked("startup")
		}
	}
	a.mu.Unlock()
	go a.runConnectionPolicy(workerCtx, done, wake)
}

func (a *Application) stopConnectionPolicy() {
	a.mu.Lock()
	a.cancelConnectionPolicyLocked()
	cancel, done := a.connectionPolicy.cancel, a.connectionPolicy.done
	if cancel != nil {
		cancel()
	}
	a.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (a *Application) resetStableConnectionLocked() {
	r := &a.connectionPolicy
	if r.readySession != nil && a.core == r.readySession && !r.readySession.stopping && !r.readyAt.IsZero() && a.connectionNowLocked().Sub(r.readyAt) >= connectionStablePeriod {
		r.attempt, r.exhausted, r.error, r.reason = 0, false, "", ""
		r.readyAt = time.Time{}
		a.publishConnectionPolicyLocked()
	}
}

func (a *Application) runConnectionPolicy(ctx context.Context, done chan struct{}, wake <-chan struct{}) {
	defer close(done)
	defer func() {
		a.mu.Lock()
		if a.connectionPolicy.ctx == ctx {
			a.cancelConnectionPolicyLocked()
		}
		a.mu.Unlock()
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		a.resetStableConnectionLocked()
		r := &a.connectionPolicy
		now := a.connectionNowLocked()
		delay := time.Duration(-1)
		if r.waiting {
			delay = max(time.Duration(0), r.nextRetryAt.Sub(now))
		}
		if !r.readyAt.IsZero() {
			stable := max(time.Duration(0), r.readyAt.Add(connectionStablePeriod).Sub(now))
			if delay < 0 || stable < delay {
				delay = stable
			}
		}
		generation, reason := r.generation, r.reason
		due := r.waiting && !r.attempting && !now.Before(r.nextRetryAt)
		a.mu.Unlock()
		if due {
			a.attemptConnectionPolicy(ctx, generation, reason)
			continue
		}
		var timer *time.Timer
		var tick <-chan time.Time
		if delay >= 0 {
			timer = time.NewTimer(delay)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-wake:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// coreControl first, then begin, then validate the generation again. A queued
// timer never bypasses busy, starts a second kernel, or consumes a retry slot.
func (a *Application) attemptConnectionPolicy(ctx context.Context, generation uint64, reason string) {
	a.coreControl.Lock()
	defer a.coreControl.Unlock()
	a.mu.Lock()
	r := &a.connectionPolicy
	valid := ctx.Err() == nil && r.generation == generation && r.wanted && r.waiting && r.reason == reason && !a.shuttingDown && !a.connectionNowLocked().Before(r.nextRetryAt)
	if !valid {
		a.mu.Unlock()
		return
	}
	if a.core != nil {
		r.waiting = false
		r.nextRetryAt = time.Time{}
		a.publishConnectionPolicyLocked()
		a.mu.Unlock()
		return
	}
	if a.operation {
		// Keep the current deadline meaningful; not a zero-duration spin loop.
		r.nextRetryAt = a.connectionNowLocked().Add(connectionBusyPoll)
		a.publishConnectionPolicyLocked()
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	if err := a.begin(); err != nil {
		a.mu.Lock()
		if r.generation == generation && r.waiting {
			if a.shuttingDown {
				a.cancelConnectionPolicyLocked()
			} else if a.backupRecoveryBlocked {
				a.exhaustConnectionPolicyLocked("配置恢复事务未完成，自动连接已停止")
			} else {
				r.nextRetryAt = a.connectionNowLocked().Add(connectionBusyPoll)
			}
		}
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	if ctx.Err() != nil || r.generation != generation || !r.wanted || !r.waiting {
		a.mu.Unlock()
		a.finish(nil)
		return
	}
	if reason == "crash" {
		if r.attempt >= a.configuredConnectionPolicyLocked().MaxRetries {
			a.exhaustConnectionPolicyLocked("已达到自动重连次数上限，请手动连接")
			a.mu.Unlock()
			a.finish(nil)
			return
		}
		r.attempt++
	}
	r.waiting, r.attempting = false, true
	r.nextRetryAt = time.Time{}
	connectCtx, cancel := context.WithCancel(ctx)
	r.connectContext, r.connectCancel = connectCtx, cancel
	start := r.start
	a.publishConnectionPolicyLocked()
	a.mu.Unlock()
	if start == nil {
		start = a.startCore
	}
	err := start()
	cancel()
	a.mu.Lock()
	r.connectContext, r.connectCancel, r.attempting = nil, nil, false
	cancelled := ctx.Err() != nil || r.generation != generation || !r.wanted
	if !cancelled && err != nil {
		// Startup is single-shot. Safety/validation failures are terminal. Other
		// readiness failures consume this crash budget and back off before retry.
		if reason == "startup" || errors.Is(err, errConnectionPolicyPermanent) || a.core != nil {
			a.exhaustConnectionPolicyLocked("自动连接失败，已停止重试；请检查连接诊断后手动连接")
		} else {
			r.error = "自动重连失败，等待下次限次重试"
			a.scheduleConnectionPolicyLocked("crash")
		}
	}
	// A ready core may already have exited in the start callback. Its exit hook
	// schedules exactly one next retry, preserving the same budget.
	a.publishConnectionPolicyLocked()
	a.mu.Unlock()
	if cancelled {
		// Cancellation may race the successful ready response. The automatic
		// attempt still owns coreControl and its operation slot, so retire its
		// kernel here instead of leaving a connected core after a busy disconnect.
		err = a.stopCore()
	}
	a.finish(err)
}

func (a *Application) connectionCoreReadyLocked(session *coreSession) {
	r := &a.connectionPolicy
	r.readySession, r.readyAt = session, a.connectionNowLocked()
	r.error = ""
	a.publishConnectionPolicyLocked()
}

func (a *Application) connectionCoreExitedLocked(session *coreSession, unexpected bool, cleanupErr error) {
	r := &a.connectionPolicy
	if session != nil && r.readySession == session {
		r.readySession, r.readyAt = nil, time.Time{}
	}
	if cleanupErr != nil {
		if r.wanted {
			a.exhaustConnectionPolicyLocked("恢复系统代理或旧内核记录失败，自动重连已停止")
		}
		return
	}
	if session == nil {
		return
	}
	if !unexpected || session.readyAt.IsZero() {
		return
	}
	if a.connectionNowLocked().Sub(session.readyAt) >= connectionStablePeriod {
		r.attempt = 0
	}
	a.scheduleConnectionPolicyLocked("crash")
}

func (a *Application) coreStartContext() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.connectionPolicy.connectContext != nil {
		return a.connectionPolicy.connectContext
	}
	if a.lifecycleContext != nil {
		return a.lifecycleContext
	}
	return context.Background()
}

// decodeStrictObject rejects ambiguous policy/patch objects without changing
// the older application's other JSON interfaces.
func decodeStrictObject(data []byte, allowed map[string]bool) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, errors.New("必须提供 JSON 对象，不能为 null")
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || !allowed[key] {
			return nil, errors.New("包含未知设置字段")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errors.New("设置字段不能重复")
		}
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err = dec.Token(); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("JSON 对象后不能有额外内容")
	}
	return fields, nil
}

func (p *ConnectionPolicy) UnmarshalJSON(data []byte) error {
	fields, err := decodeStrictObject(data, map[string]bool{"startupConnect": true, "crashReconnect": true, "maxRetries": true, "initialDelaySeconds": true, "maxDelaySeconds": true})
	if err != nil {
		return fmt.Errorf("%w：%v", errConnectionPolicy, err)
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%w：策略字段不能为 null", errConnectionPolicy)
		}
	}
	type plain ConnectionPolicy
	var value plain
	if err = json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("%w：字段类型无效", errConnectionPolicy)
	}
	normalized, err := normalizeConnectionPolicy(ConnectionPolicy(value))
	if err != nil {
		return err
	}
	*p = normalized
	return nil
}

func decodeRuntimeSettingsPatchStrict(r *http.Request) (RuntimeSettingsPatch, error) {
	var patch RuntimeSettingsPatch
	if r.Header.Get("X-Mihomo-Lite") != "1" {
		return patch, errors.New("请求校验失败，请刷新页面后重试")
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return patch, err
	}
	fields, err := decodeStrictObject(data, map[string]bool{"autoStart": true, "closeToTray": true, "silentStart": true, "ports": true, "connectionPolicy": true})
	if err != nil {
		return patch, err
	}
	if value, present := fields["connectionPolicy"]; present && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return patch, fmt.Errorf("%w：connectionPolicy 不能为 null", errConnectionPolicy)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&patch); err != nil {
		return patch, err
	}
	return patch, nil
}

// A manual connect/reconnect establishes intent in the same critical section
// as begin. Delayed goroutines may consume this token but never create one.
func (a *Application) beginManualConnectionLocked() (uint64, error) {
	if err := a.beginLocked(); err != nil {
		return 0, err
	}
	a.cancelConnectionPolicyLocked()
	a.connectionPolicy.wanted = true
	generation := a.connectionPolicy.generation
	a.connectionPolicy.operationGeneration = generation
	a.publishConnectionPolicyLocked()
	return generation, nil
}

func (a *Application) requestConnect() error {
	a.mu.Lock()
	generation, err := a.beginManualConnectionLocked()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	go func() { a.finish(a.startCoreForIntent(generation)) }()
	return nil
}

func (a *Application) requestReconnectPolicy() error {
	a.mu.Lock()
	if a.core == nil {
		a.mu.Unlock()
		return errors.New("当前未连接")
	}
	preserve := a.runtime
	_, err := a.beginManualConnectionLocked()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	go func() { a.finish(a.restartCoreForPolicy(preserve)) }()
	return nil
}

func (a *Application) startManagedCore() error {
	a.mu.Lock()
	start := a.connectionPolicy.start
	a.mu.Unlock()
	if start != nil {
		return start()
	}
	return a.startCore()
}

// The automatic attempt cleans itself up after cancellation. Other operations
// get one token-bound queued Stop; duplicate disconnect clicks are coalesced.
// A newer manual connect invalidates that queued Stop before it can claim work.
func (a *Application) requestDisconnect() error {
	a.mu.Lock()
	r := &a.connectionPolicy
	if r.disconnectPending {
		a.mu.Unlock()
		return nil
	}
	automatic := r.attempting
	a.cancelConnectionPolicyLocked()
	generation := r.generation
	if a.operation {
		if automatic {
			a.mu.Unlock()
			return nil
		}
		r.disconnectPending = true
		done := a.operationDone
		ctx := a.lifecycleContext
		if ctx == nil {
			ctx = context.Background()
		}
		a.mu.Unlock()
		go a.completeRequestedDisconnect(ctx, generation, done)
		return nil
	}
	err := a.beginLocked()
	if err == nil {
		r.disconnectPending = true
	}
	a.mu.Unlock()
	if err != nil {
		return err
	}
	go func() { a.finish(a.disconnectForIntent(generation)) }()
	return nil
}

func (a *Application) completeRequestedDisconnect(ctx context.Context, generation uint64, done <-chan struct{}) {
	for {
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return
			}
		}
		a.mu.Lock()
		r := &a.connectionPolicy
		if ctx.Err() != nil || a.shuttingDown || r.generation != generation || r.wanted {
			if r.generation == generation {
				r.disconnectPending = false
			}
			a.mu.Unlock()
			return
		}
		if a.operation {
			done = a.operationDone
			if done == nil {
				r.disconnectPending = false
				a.mu.Unlock()
				return
			}
			a.mu.Unlock()
			continue
		}
		r.disconnectPending = false
		err := a.beginLocked()
		if err == nil {
			r.disconnectPending = true
		}
		a.mu.Unlock()
		if err != nil {
			return
		}
		a.finish(a.disconnectForIntent(generation))
		return
	}
}

// Internal configuration reload owns the caller's begin/finish slot. It must
// not look like a new manual connect/disconnect or resurrect a cancelled intent.
func (a *Application) restartCoreForPolicy(preserve RuntimeConfig) error {
	a.mu.Lock()
	generation := a.connectionPolicy.operationGeneration
	if !a.operation || !a.connectionPolicy.wanted {
		a.mu.Unlock()
		return errConnectionCancelled
	}
	a.mu.Unlock()
	a.coreControl.Lock()
	defer a.coreControl.Unlock()
	a.mu.Lock()
	if a.shuttingDown || !a.connectionPolicy.wanted || a.connectionPolicy.generation != generation {
		a.mu.Unlock()
		return errConnectionCancelled
	}
	a.mu.Unlock()
	if err := a.stopCore(); err != nil {
		return err
	}
	a.mu.Lock()
	if a.shuttingDown || !a.connectionPolicy.wanted || a.connectionPolicy.generation != generation {
		a.mu.Unlock()
		return errConnectionCancelled
	}
	a.runtime = preserve
	parent := a.lifecycleContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	a.connectionPolicy.connectContext, a.connectionPolicy.connectCancel = ctx, cancel
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		a.connectionPolicy.connectContext, a.connectionPolicy.connectCancel = nil, nil
		a.mu.Unlock()
	}()
	err := a.startManagedCore()
	a.mu.Lock()
	cancelled := a.shuttingDown || !a.connectionPolicy.wanted || a.connectionPolicy.generation != generation
	a.mu.Unlock()
	if cancelled {
		return a.stopCore()
	}
	return err
}
